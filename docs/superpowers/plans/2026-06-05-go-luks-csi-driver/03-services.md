# LUKS CSI Driver — Phase 3: CSI services & wiring (Tasks 10–15)

> Part of the [Go LUKS CSI Driver plan](00-index.md). Depends on Phases 1–2 ([01-foundation.md](01-foundation.md), [02-domain.md](02-domain.md)).

**Covers:** `identity`, `controller`, `node` CSI services, `server` gRPC bootstrap, `cmd/luks-csi` entrypoint, per-mode dependency wiring.

---

## Task 10: `identity` CSI service

The Identity service is thin: advertise the driver name/version and capabilities, and report readiness via `Probe`.

**Files:** Create `internal/identity/identity.go`, `internal/identity/identity_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/identity/identity_test.go`)

```go
package identity

import (
	"context"
	"testing"
)

func TestGetPluginInfoReturnsDriverName(t *testing.T) {
	s := New("v0.1.0")
	info, err := s.GetPluginInfo(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetPluginInfo: %v", err)
	}
	if info.GetName() != "luks.csi.nfcs.io" {
		t.Fatalf("name = %q, want luks.csi.nfcs.io", info.GetName())
	}
	if info.GetVendorVersion() != "v0.1.0" {
		t.Fatalf("version = %q, want v0.1.0", info.GetVendorVersion())
	}
}

func TestProbeReady(t *testing.T) {
	s := New("v0.1.0")
	resp, err := s.Probe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !resp.GetReady().GetValue() {
		t.Fatal("Probe reported not ready")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/identity/ -run TestGetPluginInfoReturnsDriverName -v` — Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Write minimal implementation** (`internal/identity/identity.go`)

```go
// Package identity implements the CSI Identity service.
package identity

import (
	"context"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// DriverName is the CSI driver name (own name; greenfield, not the Python one).
const DriverName = "luks.csi.nfcs.io"

// Service implements csi.IdentityServer.
type Service struct {
	csi.UnimplementedIdentityServer
	version string
}

func New(version string) *Service { return &Service{version: version} }

func (s *Service) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{Name: DriverName, VendorVersion: s.version}, nil
}

func (s *Service) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{
				Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
			}}},
		},
	}, nil
}

func (s *Service) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{Ready: wrapperspb.Bool(true)}, nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/identity/ -v` — Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/identity/
git commit -m "feat(identity): CSI Identity service"
```

---

## Task 11: `controller` CSI service

Addresses **M-1** (PVC-name collision — backing names are `luks-backing-` + SHA-256 suffix), **M-6** (destructive delete default — `deletionPolicy` defaults to `Retain`, `DeleteVolume` is a no-op on the Vault key unless `Delete`), and composes `vault.EnsureKey` + `device.Attach`. Errors cross the boundary via `csierr`.

**Files:** Create `internal/controller/controller.go`, `internal/controller/naming.go`, `internal/controller/controller_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/controller/controller_test.go`)

```go
package controller

import (
	"context"
	"strings"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBackingNameIsCollisionResistant(t *testing.T) {
	a := backingName("default", "pvc-aaaaaaaa")
	b := backingName("default", "pvc-bbbbbbbb")
	if a == b {
		t.Fatal("distinct volumes produced identical backing names")
	}
	if !strings.HasPrefix(a, "luks-backing-") {
		t.Fatalf("backing name %q lacks luks-backing- prefix", a)
	}
	// Deterministic for the same inputs.
	if a != backingName("default", "pvc-aaaaaaaa") {
		t.Fatal("backingName is not deterministic")
	}
}

func TestCreateVolumeRequiresName(t *testing.T) {
	c := New(Deps{})
	_, err := c.CreateVolume(context.Background(), &csi.CreateVolumeRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestCreateVolumeDefaultsToRetain(t *testing.T) {
	fk := &fakeVault{}
	c := New(Deps{Vault: fk, Backing: &fakeBacking{}})
	resp, err := c.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name: "pvc-abc",
		Parameters: map[string]string{
			"institution":        "bristol",
			"backingStorageClass": "ceph-block",
		},
	})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if got := resp.GetVolume().GetVolumeContext()["deletionPolicy"]; got != "Retain" {
		t.Fatalf("deletionPolicy = %q, want Retain (M-6 default)", got)
	}
	if !fk.ensured {
		t.Fatal("CreateVolume did not ensure a Vault key")
	}
}

func TestDeleteVolumeRetainKeepsKey(t *testing.T) {
	fk := &fakeVault{}
	c := New(Deps{Vault: fk, Backing: &fakeBacking{}})
	_, err := c.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{
		VolumeId: "bristol/pvc-abc/Retain",
	})
	if err != nil {
		t.Fatalf("DeleteVolume: %v", err)
	}
	if fk.deleted {
		t.Fatal("DeleteVolume deleted the key despite Retain policy")
	}
}

func TestDeleteVolumeDeleteRemovesKey(t *testing.T) {
	fk := &fakeVault{}
	c := New(Deps{Vault: fk, Backing: &fakeBacking{}})
	_, err := c.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{
		VolumeId: "bristol/pvc-abc/Delete",
	})
	if err != nil {
		t.Fatalf("DeleteVolume: %v", err)
	}
	if !fk.deleted {
		t.Fatal("DeleteVolume did not delete the key under Delete policy")
	}
}

// --- fakes ---

type fakeVault struct {
	ensured bool
	deleted bool
}

func (f *fakeVault) EnsureKey(_ context.Context, _, _ string) error { f.ensured = true; return nil }
func (f *fakeVault) DeleteKey(_ context.Context, _, _ string) error { f.deleted = true; return nil }

type fakeBacking struct{}

func (f *fakeBacking) CreateBackingPVC(_ context.Context, name, sc string) (string, error) {
	return "pv-for-" + name, nil
}
func (f *fakeBacking) DeleteBackingPVC(_ context.Context, name string) error { return nil }
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/controller/ -run TestBackingNameIsCollisionResistant -v` — Expected: FAIL, `undefined: backingName`.

- [ ] **Step 3: Write the naming helper** (`internal/controller/naming.go`)

```go
package controller

import (
	"crypto/sha256"
	"encoding/hex"
)

// backingName derives a collision-resistant backing-PVC name (M-1).
func backingName(namespace, volumeName string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + volumeName))
	return "luks-backing-" + hex.EncodeToString(sum[:16])
}
```

- [ ] **Step 4: Write the controller** (`internal/controller/controller.go`)

```go
// Package controller implements the CSI Controller service.
package controller

import (
	"context"
	"fmt"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"

	"github.com/nfcs/k8s-luks-csi-go/internal/csierr"
	"github.com/nfcs/k8s-luks-csi-go/internal/validate"
)

// VaultKeyer is the controller's view of the Vault client.
type VaultKeyer interface {
	EnsureKey(ctx context.Context, institution, volume string) error
	DeleteKey(ctx context.Context, institution, volume string) error
}

// Backing provisions/removes the backing PVC.
type Backing interface {
	CreateBackingPVC(ctx context.Context, name, storageClass string) (pvName string, err error)
	DeleteBackingPVC(ctx context.Context, name string) error
}

// Deps are the controller's collaborators.
type Deps struct {
	Vault   VaultKeyer
	Backing Backing
}

// Service implements csi.ControllerServer.
type Service struct {
	csi.UnimplementedControllerServer
	deps Deps
}

func New(deps Deps) *Service { return &Service{deps: deps} }

func (s *Service) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	name := req.GetName()
	if name == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "create volume", fmt.Errorf("missing name"))
	}
	if err := validate.VolumeName(name); err != nil {
		return nil, csierr.Wrap(codes.InvalidArgument, "create volume", err)
	}
	params := req.GetParameters()
	institution := params["institution"]
	if err := validate.Institution(institution); err != nil {
		return nil, csierr.Wrap(codes.InvalidArgument, "create volume", err)
	}
	sc := params["backingStorageClass"]
	if sc == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "create volume", fmt.Errorf("missing backingStorageClass"))
	}

	// M-6: default reclaim/deletion policy is Retain.
	policy := params["deletionPolicy"]
	if policy == "" {
		policy = "Retain"
	}

	backing := backingName(institution, name)
	pvName, err := s.deps.Backing.CreateBackingPVC(ctx, backing, sc)
	if err != nil {
		return nil, csierr.Wrap(codes.Internal, "create backing pvc", err)
	}
	if err := s.deps.Vault.EnsureKey(ctx, institution, name); err != nil {
		return nil, csierr.Wrap(codes.Internal, "ensure key", err)
	}

	volID := fmt.Sprintf("%s/%s/%s", institution, name, policy)
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volID,
			CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
			VolumeContext: map[string]string{
				"backingPV":      pvName,
				"institution":    institution,
				"luksType":       "luks2",
				"deletionPolicy": policy,
			},
		},
	}, nil
}

// parseVolumeID splits "<institution>/<volume>/<policy>".
func parseVolumeID(id string) (institution, volume, policy string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("malformed volume id %q", id)
	}
	return parts[0], parts[1], parts[2], nil
}

func (s *Service) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	institution, volume, policy, err := parseVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, csierr.Wrap(codes.InvalidArgument, "delete volume", err)
	}
	if policy == "Delete" {
		if err := s.deps.Vault.DeleteKey(ctx, institution, volume); err != nil {
			return nil, csierr.Wrap(codes.Internal, "delete key", err)
		}
		if err := s.deps.Backing.DeleteBackingPVC(ctx, backingName(institution, volume)); err != nil {
			return nil, csierr.Wrap(codes.Internal, "delete backing pvc", err)
		}
	}
	// Retain (default): no-op on the Vault key (M-6).
	return &csi.DeleteVolumeResponse{}, nil
}

func (s *Service) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	caps := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
	}
	var out []*csi.ControllerServiceCapability
	for _, c := range caps {
		out = append(out, &csi.ControllerServiceCapability{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{Type: c},
			},
		})
	}
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: out}, nil
}
```

> **Executor note:** `ControllerPublishVolume`/`ControllerUnpublishVolume` wire `device.Attach`/`Detach` for attach-based backends and no-op for map-on-stage backends (spec §8). Add them with a `device.Attacher` field on `Deps` and a fake in the test (mirroring `fakeBacking`) once Task 9 is integrated — they are not needed to make the CreateVolume/DeleteVolume tests pass. **H-11** (narrow PVC RBAC) is enforced in the Helm RBAC (Task 16), not in code.

- [ ] **Step 5: Run tests, verify they pass**

Run: `go test ./internal/controller/ -v` — Expected: PASS (5 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/controller/
git commit -m "feat(controller): CreateVolume/DeleteVolume with collision-resistant naming + Retain default"
```

---

## Task 12: `node` CSI service

Composes everything at stage time: read the per-tenant Vault credential from `request.secrets`, resolve+attach the device, fetch the key into a `keyzero.Buffer`, format-or-open (with rotation), mount, and **zero the key in `defer`**. Addresses **H-7** (no key material or key-file paths logged; structured `slog`). The orchestration is tested with fakes for luks/vault/device.

**Files:** Create `internal/node/node.go`, `internal/node/node_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/node/node_test.go`)

```go
package node

import (
	"context"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
)

func TestNodeStageRequiresTenantSecret(t *testing.T) {
	s := New(Deps{})
	_, err := s.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "bristol/pvc-1/Retain",
		StagingTargetPath: "/var/lib/kubelet/stage/pvc-1",
		VolumeContext:     map[string]string{"institution": "bristol", "backend": "by-id"},
		// no secrets => must be rejected (H-1: node has no standing creds)
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestNodeStageFormatsWhenNotLUKSAndZeroesKey(t *testing.T) {
	fk := &fakeKeySource{key: "0123456789abcdef0123456789abcdef"}
	fl := &fakeLUKS{isLUKS: false}
	s := New(Deps{
		Keys:   fk,
		Luks:   fl,
		Device: &fakeDevice{path: "/dev/disk/by-id/x"},
		Mount:  &fakeMounter{},
	})
	_, err := s.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "bristol/pvc-1/Retain",
		StagingTargetPath: "/var/lib/kubelet/stage/pvc-1",
		VolumeContext:     map[string]string{"institution": "bristol", "backend": "by-id"},
		Secrets:           map[string]string{"roleId": "r", "secretId": "s"},
	})
	if err != nil {
		t.Fatalf("NodeStageVolume: %v", err)
	}
	if !fl.formatted {
		t.Fatal("expected Format on a non-LUKS device")
	}
	if !fl.madeFS {
		t.Fatal("expected MakeFS after formatting a fresh device")
	}
	if !fk.zeroed {
		t.Fatal("key buffer was not zeroed after staging (H-4/M-4)")
	}
}

func TestNodeStageOpensExistingLUKSWithoutFormat(t *testing.T) {
	fk := &fakeKeySource{key: "0123456789abcdef0123456789abcdef"}
	fl := &fakeLUKS{isLUKS: true}
	s := New(Deps{Keys: fk, Luks: fl, Device: &fakeDevice{path: "/dev/x"}, Mount: &fakeMounter{}})
	_, err := s.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "bristol/pvc-1/Retain",
		StagingTargetPath: "/stage",
		VolumeContext:     map[string]string{"institution": "bristol", "backend": "by-id"},
		Secrets:           map[string]string{"roleId": "r", "secretId": "s"},
	})
	if err != nil {
		t.Fatalf("NodeStageVolume: %v", err)
	}
	if fl.formatted {
		t.Fatal("must not Format an existing LUKS device")
	}
	if !fl.opened {
		t.Fatal("expected Open on an existing LUKS device")
	}
}

// --- fakes ---

type fakeKeySource struct {
	key    string
	zeroed bool
}

func (f *fakeKeySource) KeyFor(_ context.Context, _ Secret, _, _ string) (*keyzero.Buffer, error) {
	b, _ := keyzero.NewBuffer(len(f.key))
	copy(b.Bytes(), []byte(f.key))
	// wrap so we can observe zeroing
	return b, nil
}

type fakeLUKS struct {
	isLUKS              bool
	formatted, opened   bool
	madeFS              bool
}

func (f *fakeLUKS) IsLUKS(context.Context, string) (bool, error) { return f.isLUKS, nil }
func (f *fakeLUKS) Format(context.Context, string, *keyzero.Buffer) error {
	f.formatted = true
	return nil
}
func (f *fakeLUKS) Open(context.Context, string, string, *keyzero.Buffer) error {
	f.opened = true
	return nil
}
func (f *fakeLUKS) MakeFS(context.Context, string) error { f.madeFS = true; return nil }

type fakeDevice struct{ path string }

func (f *fakeDevice) ResolveAndAttach(context.Context, map[string]string) (string, error) {
	return f.path, nil
}

type fakeMounter struct{}

func (f *fakeMounter) Mount(_ context.Context, _, _ string) error { return nil }
```

> The `fakeKeySource` should set `f.zeroed = true` from a wrapper. Simplest: change `KeyFor` to return a buffer and have the node code call `buf.Zero()`; assert zeroing by having the fake hand back a buffer it retains and checking its bytes are zero after the call. Adjust the fake to keep a reference: store `f.buf = b; return b, nil`, then in the test assert `allZero(fk.buf.Bytes())`. Update the assertion accordingly when implementing.

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/node/ -run TestNodeStageRequiresTenantSecret -v` — Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Write minimal implementation** (`internal/node/node.go`)

```go
// Package node implements the CSI Node service.
package node

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"

	"github.com/nfcs/k8s-luks-csi-go/internal/csierr"
	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
	"github.com/nfcs/k8s-luks-csi-go/internal/validate"
)

// Secret is the per-tenant Vault credential delivered via node-stage secrets.
type Secret struct {
	RoleID   string
	SecretID string
}

// KeySource fetches a volume's key scoped to the tenant credential.
type KeySource interface {
	KeyFor(ctx context.Context, s Secret, institution, volume string) (*keyzero.Buffer, error)
}

// LUKSOps is the node's view of the luks package.
type LUKSOps interface {
	IsLUKS(ctx context.Context, device string) (bool, error)
	Format(ctx context.Context, device string, key *keyzero.Buffer) error
	Open(ctx context.Context, device, name string, key *keyzero.Buffer) error
	MakeFS(ctx context.Context, name string) error
}

// DeviceOps resolves+attaches the backing device.
type DeviceOps interface {
	ResolveAndAttach(ctx context.Context, volumeContext map[string]string) (string, error)
}

// Mounter mounts the opened mapper device at the staging path.
type Mounter interface {
	Mount(ctx context.Context, source, target string) error
}

// Deps are the node service's collaborators.
type Deps struct {
	Keys   KeySource
	Luks   LUKSOps
	Device DeviceOps
	Mount  Mounter
}

// Service implements csi.NodeServer.
type Service struct {
	csi.UnimplementedNodeServer
	deps Deps
}

func New(deps Deps) *Service { return &Service{deps: deps} }

func (s *Service) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	institution, volume, _, err := parseVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, csierr.Wrap(codes.InvalidArgument, "node stage", err)
	}
	secrets := req.GetSecrets()
	roleID, secretID := secrets["roleId"], secrets["secretId"]
	if roleID == "" || secretID == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "node stage",
			fmt.Errorf("missing per-tenant vault credential in node-stage secrets"))
	}

	device, err := s.deps.Device.ResolveAndAttach(ctx, req.GetVolumeContext())
	if err != nil {
		return nil, csierr.Wrap(codes.Internal, "resolve device", err)
	}

	key, err := s.deps.Keys.KeyFor(ctx, Secret{RoleID: roleID, SecretID: secretID}, institution, volume)
	if err != nil {
		return nil, csierr.Wrap(codes.Internal, "fetch key", err)
	}
	defer key.Zero() // H-4/M-4: never cached across volumes

	mapper, err := mapperName(institution, volume)
	if err != nil {
		return nil, csierr.Wrap(codes.Internal, "node stage", err)
	}

	isLUKS, err := s.deps.Luks.IsLUKS(ctx, device)
	if err != nil {
		return nil, csierr.Wrap(codes.Internal, "probe luks", err)
	}
	if !isLUKS {
		if err := s.deps.Luks.Format(ctx, device, key); err != nil {
			return nil, csierr.Wrap(codes.Internal, "format", err)
		}
		if err := s.deps.Luks.Open(ctx, device, mapper, key); err != nil {
			return nil, csierr.Wrap(codes.Internal, "open", err)
		}
		if err := s.deps.Luks.MakeFS(ctx, mapper); err != nil {
			return nil, csierr.Wrap(codes.Internal, "mkfs", err)
		}
	} else {
		if err := s.deps.Luks.Open(ctx, device, mapper, key); err != nil {
			// Rotation handled here in production (Task 7 FindAuthenticatingVersion).
			return nil, csierr.Wrap(codes.Internal, "open", err)
		}
	}

	if err := s.deps.Mount.Mount(ctx, "/dev/mapper/"+mapper, req.GetStagingTargetPath()); err != nil {
		return nil, csierr.Wrap(codes.Internal, "mount", err)
	}
	slog.Info("staged volume", "institution", institution, "volume", volume, "mapper", mapper)
	return &csi.NodeStageVolumeResponse{}, nil
}

// parseVolumeID parses "<institution>/<volume>/<policy>".
func parseVolumeID(id string) (institution, volume, policy string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("malformed volume id %q", id)
	}
	return parts[0], parts[1], parts[2], nil
}

// mapperName derives the validated /dev/mapper target (H-5).
func mapperName(institution, volume string) (string, error) {
	name := "luks-" + sha256hex16(institution+"\x00"+volume)
	if err := validate.MapperName(name); err != nil {
		return "", err
	}
	return name, nil
}

func (s *Service) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{
				Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
			}}},
		},
	}, nil
}
```

Add the SHA-256 helper (`internal/node/hash.go`):

```go
package node

import (
	"crypto/sha256"
	"encoding/hex"
)

func sha256hex16(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16]) // 32 hex chars => matches mapper regex
}
```

> **Executor note:** Implement `NodeUnstageVolume` (umount with lazy fallback → `luksClose` → check `dmsetup` return codes for no-false-success → detach), `NodePublishVolume`/`NodeUnpublishVolume` (bind-mount staging→target, device-specific only per **M-3**), and `NodeGetInfo` following the same fake-driven test pattern. Production `KeyFor` does `vault.LoginAppRole` then `ReadKey`; on a wrong-key `Open` it calls `vault.FindAuthenticatingVersion` (Task 7) with a `tryOpen` closure that shells `luksOpen --test-passphrase`. Never log key bytes or key-file paths (H-7). **DRY:** `parseVolumeID` is now identical in `controller` and `node`; if a third caller appears, extract it to a tiny `internal/volumeid` package — for two call sites the 3-line duplication is acceptable.

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/node/ -v` — Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/node/
git commit -m "feat(node): NodeStageVolume orchestration with key zeroing"
```

---

## Task 13: `server` — gRPC bootstrap over Unix socket

Listens on a Unix socket, registers the services for the selected `--mode`, and supports clean shutdown. **gRPC-Go (confirmed via context7):** `net.Listen("unix", path)` → `grpc.NewServer()` → `csi.Register*Server(s, impl)` → `s.Serve(lis)`.

**Files:** Create `internal/server/server.go`, `internal/server/server_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/server/server_test.go`)

The server is exercised end-to-end over a real Unix socket using a gRPC client calling `GetPluginInfo`.

```go
package server

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/nfcs/k8s-luks-csi-go/internal/identity"
)

func TestServerServesIdentityOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "csi.sock")
	srv := New(Config{Endpoint: "unix://" + sock, Mode: ModeController, Version: "v0.0.1"})
	srv.identity = identity.New("v0.0.1")

	go func() { _ = srv.Run() }()
	defer srv.Stop()

	conn, err := grpc.NewClient("unix://"+sock,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Wait briefly for Serve to bind.
	deadline := time.Now().Add(2 * time.Second)
	var info *csi.GetPluginInfoResponse
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		info, err = csi.NewIdentityClient(conn).GetPluginInfo(ctx, &csi.GetPluginInfoRequest{})
		cancel()
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GetPluginInfo: %v", err)
	}
	if info.GetName() != "luks.csi.nfcs.io" {
		t.Fatalf("name = %q", info.GetName())
	}
	_ = net.Dial // keep net import if trimmed
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/server/ -run TestServerServesIdentityOverUnixSocket -v` — Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Write minimal implementation** (`internal/server/server.go`)

```go
// Package server bootstraps the CSI gRPC services over a Unix socket.
package server

import (
	"fmt"
	"net"
	"os"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"

	"github.com/nfcs/k8s-luks-csi-go/internal/controller"
	"github.com/nfcs/k8s-luks-csi-go/internal/identity"
	"github.com/nfcs/k8s-luks-csi-go/internal/node"
)

// Mode selects which services to register.
type Mode string

const (
	ModeController Mode = "controller"
	ModeNode       Mode = "node"
	ModeAll        Mode = "all"
)

// Config configures the server.
type Config struct {
	Endpoint string // unix://<path>
	Mode     Mode
	Version  string
}

// Server owns the gRPC server and its listener.
type Server struct {
	cfg        Config
	grpc       *grpc.Server
	identity   *identity.Service
	controller *controller.Service
	node       *node.Service
}

// New constructs a Server. Service deps are wired by the caller (main) before Run.
func New(cfg Config) *Server {
	return &Server{cfg: cfg, grpc: grpc.NewServer()}
}

// WithControllerDeps / WithNodeDeps set the composed services.
func (s *Server) WithController(c *controller.Service) *Server { s.controller = c; return s }
func (s *Server) WithNode(n *node.Service) *Server             { s.node = n; return s }
func (s *Server) WithIdentity(i *identity.Service) *Server     { s.identity = i; return s }

func (s *Server) Run() error {
	network, addr, err := parseEndpoint(s.cfg.Endpoint)
	if err != nil {
		return err
	}
	if network == "unix" {
		_ = os.Remove(addr) // clear stale socket
	}
	lis, err := net.Listen(network, addr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", s.cfg.Endpoint, err)
	}

	if s.identity == nil {
		s.identity = identity.New(s.cfg.Version)
	}
	csi.RegisterIdentityServer(s.grpc, s.identity)
	if s.cfg.Mode == ModeController || s.cfg.Mode == ModeAll {
		if s.controller != nil {
			csi.RegisterControllerServer(s.grpc, s.controller)
		}
	}
	if s.cfg.Mode == ModeNode || s.cfg.Mode == ModeAll {
		if s.node != nil {
			csi.RegisterNodeServer(s.grpc, s.node)
		}
	}
	return s.grpc.Serve(lis)
}

func (s *Server) Stop() { s.grpc.GracefulStop() }

func parseEndpoint(ep string) (network, addr string, err error) {
	if strings.HasPrefix(ep, "unix://") {
		return "unix", strings.TrimPrefix(ep, "unix://"), nil
	}
	if strings.HasPrefix(ep, "tcp://") {
		return "tcp", strings.TrimPrefix(ep, "tcp://"), nil
	}
	return "", "", fmt.Errorf("server: unsupported endpoint %q", ep)
}
```

> **Executor note:** The test sets `srv.identity` directly (same package). In production `main` uses the exported `WithIdentity/WithController/WithNode` builders. If you prefer not to expose the field for the test, switch the test to `srv.WithIdentity(identity.New("v0.0.1"))`.

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/server/ -v` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/
git commit -m "feat(server): gRPC bootstrap over Unix socket with mode selection"
```

---

## Task 14: `cmd/luks-csi` entrypoint

Wires the `--mode`/`--endpoint` flags, sets up structured `slog` (level from `LOG_LEVEL`, default INFO — **H-7**), builds the dependency graph, and starts the server.

**Files:** Create `cmd/luks-csi/main.go`.

- [ ] **Step 1: Write the implementation** (`cmd/luks-csi/main.go`)

This is the composition root — no unit test (covered by the sanity suite in Task 18). Keep it thin.

```go
// Command luks-csi is the LUKS CSI driver entrypoint.
package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/nfcs/k8s-luks-csi-go/internal/server"
)

var version = "dev" // set via -ldflags at build time

func main() {
	var (
		endpoint = flag.String("endpoint", "unix:///csi/csi.sock", "CSI gRPC endpoint")
		mode     = flag.String("mode", "all", "controller | node | all")
	)
	flag.Parse()

	setupLogging()

	srv := server.New(server.Config{
		Endpoint: *endpoint,
		Mode:     server.Mode(*mode),
		Version:  version,
	})

	// NOTE: dependency wiring (vault.New, device registry, luks runners,
	// client-go in-cluster config) is assembled here per mode and attached
	// via srv.WithController(...) / srv.WithNode(...). See Task 15.

	slog.Info("starting luks-csi", "mode", *mode, "endpoint", *endpoint, "version", version)
	if err := srv.Run(); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func setupLogging() {
	level := slog.LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		_ = level.UnmarshalText([]byte(v))
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
}
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./cmd/luks-csi/` — Expected: builds, produces no errors.

- [ ] **Step 3: Smoke-test the binary boots and serves Identity**

Run:
```bash
go run ./cmd/luks-csi -mode controller -endpoint unix:///tmp/csi-smoke.sock &
sleep 1
# Identity should answer; then stop it.
kill %1
```
Expected: log line `starting luks-csi ... mode=controller`, no crash.

- [ ] **Step 4: Commit**

```bash
git add cmd/luks-csi/
git commit -m "feat(cmd): luks-csi entrypoint with slog and mode flag"
```

---

## Task 15: Dependency wiring per mode

Assemble the real collaborators in `main` (or a small `internal/app` builder) so controller and node modes get fully-wired services. This is the integration seam between all prior packages.

**Files:** Create `internal/app/wire.go`, `internal/app/wire_test.go`. Modify `cmd/luks-csi/main.go` to call the builder.

- [ ] **Step 1: Write the failing test** (`internal/app/wire_test.go`)

```go
package app

import "testing"

func TestBuildControllerRequiresVaultConfig(t *testing.T) {
	// Missing VAULT_ADDR must fail fast (C-1: no insecure default).
	_, err := BuildController(Config{VaultAddr: "", VaultCABundle: "/tmp/ca"})
	if err == nil {
		t.Fatal("BuildController accepted an empty Vault address")
	}
}

func TestBuildControllerRejectsHTTPVault(t *testing.T) {
	_, err := BuildController(Config{VaultAddr: "http://v:8200", VaultCABundle: "/tmp/ca"})
	if err == nil {
		t.Fatal("BuildController accepted a plaintext Vault address")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/app/ -run TestBuildControllerRequiresVaultConfig -v` — Expected: FAIL, `undefined: BuildController`.

- [ ] **Step 3: Write minimal implementation** (`internal/app/wire.go`)

```go
// Package app assembles the dependency graph for each driver mode.
package app

import (
	"fmt"

	"github.com/nfcs/k8s-luks-csi-go/internal/controller"
	"github.com/nfcs/k8s-luks-csi-go/internal/vault"
)

// Config holds runtime configuration sourced from env/flags.
type Config struct {
	VaultAddr     string
	VaultCABundle string
}

// BuildController wires the Controller service. Fails fast if the Vault
// transport is not https with a CA bundle (C-1).
func BuildController(cfg Config) (*controller.Service, error) {
	if cfg.VaultAddr == "" {
		return nil, fmt.Errorf("app: VAULT_ADDR is required")
	}
	vc, err := vault.New(vault.Config{Address: cfg.VaultAddr, CABundlePath: cfg.VaultCABundle})
	if err != nil {
		return nil, err
	}
	// backingProvisioner implements controller.Backing over client-go (added
	// alongside ControllerPublishVolume wiring); nil-safe for the config test.
	return controller.New(controller.Deps{Vault: vc}), nil
}
```

> **Executor note:** Add `BuildNode(cfg)` symmetrically (luks runners via `luks.New(luks.NewExecRunner("cryptsetup"))`, `device.Registry` with all resolvers + `device.NewAttacher(clientset)`, a real `KeySource` that does `LoginAppRole`+`ReadKey`, and a `Mounter` over `k8s.io/mount-utils`). Source `Config` fields from env (`VAULT_ADDR`, `VAULT_CACERT`). Build the client-go `*kubernetes.Clientset` from `rest.InClusterConfig()` (auto-refreshes the projected SA token). Update `main.go` Task 14 step-1 NOTE block to call `BuildController`/`BuildNode` and attach via `srv.WithController`/`srv.WithNode`.

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/app/ -v` — Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/app/ cmd/luks-csi/main.go
git commit -m "feat(app): per-mode dependency wiring with fail-fast Vault TLS"
```

---

