# Build-Time Debug Logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `debug` build-tag-gated constant that lets the compiler eliminate all debug log blocks in production binaries, then instrument the four key call paths with sanitised production logs and full-detail debug logs.

**Architecture:** A two-file `internal/dbg` package exposes `const Enabled bool`. When built without `-tags debug`, `Enabled = false` and every `if dbg.Enabled { ... }` block is dead code. Each instrumented file also gets sanitised INFO/WARN production logs containing no identifiers, replacing any existing logs that leak institution names, paths, or PVC names.

**Tech Stack:** Go 1.25, `log/slog` (stdlib), build tags (`//go:build`)

---

## File Map

| Action | File | What changes |
|---|---|---|
| Create | `internal/dbg/off.go` | `const Enabled = false` behind `!debug` tag |
| Create | `internal/dbg/on.go` | `const Enabled = true` behind `debug` tag |
| Modify | `internal/controller/controller.go` | sanitised INFO/WARN + debug guards; replace raw slog.Error |
| Modify | `internal/app/backing.go` | sanitised WARN + debug guards with full PVC detail |
| Modify | `internal/vault/rotate.go` | sanitised WARN + debug guards for EnsureKey/DeleteKey |
| Modify | `internal/node/node.go` | remove identifier-leaking slog.Info; sanitised INFO + debug guards |
| Modify | `Dockerfile` | `ARG DEBUG_BUILD` wired into build stage RUN |

---

## Task 1: Create `internal/dbg` package

**Files:**
- Create: `internal/dbg/off.go`
- Create: `internal/dbg/on.go`

- [ ] **Step 1: Create off.go**

```go
// internal/dbg/off.go
//go:build !debug

package dbg

// Enabled is false in production builds. Every `if dbg.Enabled { ... }` block
// is dead code — the compiler removes it entirely, including argument evaluation.
const Enabled = false
```

- [ ] **Step 2: Create on.go**

```go
// internal/dbg/on.go
//go:build debug

package dbg

// Enabled is true in debug builds (go build -tags debug).
// Debug logs may contain sensitive identifiers and must never be used in production.
const Enabled = true
```

- [ ] **Step 3: Verify both files compile cleanly**

```shell
cd /Users/hd25646/Documents/NFCS-secure-k8s/k8s-luks-csi-go/.claude/worktrees/feat+go-luks-csi-driver
go build ./internal/dbg/
go build -tags debug ./internal/dbg/
```

Expected: no output, exit 0 for both.

- [ ] **Step 4: Commit**

```shell
git add internal/dbg/off.go internal/dbg/on.go
git commit -m "feat(dbg): add build-time debug constant

Enabled=false in production; if dbg.Enabled blocks are dead code.
Enabled=true with -tags debug only."
```

---

## Task 2: Instrument `internal/controller/controller.go`

**Files:**
- Modify: `internal/controller/controller.go`

The current file has two `slog.Error` calls added during debugging that expose the volume name and raw error — both must be replaced. Add sanitised INFO at start/success and WARN on failure. Add debug guards with full detail.

- [ ] **Step 1: Replace the import block and full CreateVolume function**

Replace the entire file's import block and `CreateVolume` function with:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"

	"github.com/nfcs/k8s-luks-csi-go/internal/csierr"
	"github.com/nfcs/k8s-luks-csi-go/internal/dbg"
	"github.com/nfcs/k8s-luks-csi-go/internal/validate"
)
```

Replace `CreateVolume` with:

```go
func (s *Service) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	slog.Info("CreateVolume called")
	name := req.GetName()
	if name == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "create volume", fmt.Errorf("missing name"))
	}
	if err := validate.VolumeName(name); err != nil {
		return nil, csierr.Wrap(codes.InvalidArgument, "create volume", err)
	}
	if len(req.GetVolumeCapabilities()) == 0 {
		return nil, csierr.Wrap(codes.InvalidArgument, "create volume", fmt.Errorf("missing volume capabilities"))
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
	policy := params["deletionPolicy"]
	if policy == "" {
		policy = "Retain"
	}

	if dbg.Enabled {
		slog.Debug("CreateVolume params",
			"institution", institution,
			"backingStorageClass", sc,
			"deletionPolicy", policy,
		)
	}

	backing := backingName(institution, name)
	if dbg.Enabled {
		slog.Debug("CreateVolume backing name derived", "backing", backing)
	}

	pvName, err := s.deps.Backing.CreateBackingPVC(ctx, backing, sc)
	if err != nil {
		slog.Warn("CreateVolume failed", "op", "create backing pvc")
		return nil, csierr.Wrap(codes.Internal, "create backing pvc", err)
	}
	if err := s.deps.Vault.EnsureKey(ctx, institution, name); err != nil {
		slog.Warn("CreateVolume failed", "op", "ensure key")
		return nil, csierr.Wrap(codes.Internal, "ensure key", err)
	}

	volID := fmt.Sprintf("%s/%s/%s", institution, name, policy)
	volumeCtx := map[string]string{
		"backingPV":      pvName,
		"institution":    institution,
		"luksType":       "luks2",
		"deletionPolicy": policy,
	}
	for _, k := range []string{"devicePath", "backend"} {
		if v := params[k]; v != "" {
			volumeCtx[k] = v
		}
	}

	if dbg.Enabled {
		slog.Debug("CreateVolume ok", "volumeID", volID, "backingPV", pvName)
	}
	slog.Info("CreateVolume ok")
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volID,
			CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
			VolumeContext: volumeCtx,
		},
	}, nil
}
```

- [ ] **Step 2: Build and verify no compile errors**

```shell
go build ./internal/controller/
go build -tags debug ./internal/controller/
```

Expected: no output, exit 0.

- [ ] **Step 3: Run existing tests**

```shell
go test ./internal/controller/
```

Expected: `ok  github.com/nfcs/k8s-luks-csi-go/internal/controller`

- [ ] **Step 4: Commit**

```shell
git add internal/controller/controller.go
git commit -m "feat(controller): sanitised logs + build-time debug guards in CreateVolume

Production logs contain no identifiers. Debug logs (build tag: debug) add
institution, backingStorageClass, backing name, and volumeID."
```

---

## Task 3: Instrument `internal/app/backing.go`

**Files:**
- Modify: `internal/app/backing.go`

Add a `start` timer for bind duration in debug mode, sanitised WARN on failure, and debug guards showing namespace, PVC name, storageClass, error detail, PV name, and poll ticks.

- [ ] **Step 1: Replace backing.go with the instrumented version**

```go
package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/nfcs/k8s-luks-csi-go/internal/dbg"
)

type backingProvisioner struct {
	cs        kubernetes.Interface
	namespace string
}

func (b *backingProvisioner) CreateBackingPVC(ctx context.Context, name, storageClass string) (string, error) {
	if dbg.Enabled {
		slog.Debug("backing: creating pvc", "ns", b.namespace, "name", name, "sc", storageClass)
	}
	blockMode := corev1.PersistentVolumeBlock
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: b.namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &storageClass,
			VolumeMode:       &blockMode,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
	_, err := b.cs.CoreV1().PersistentVolumeClaims(b.namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		if dbg.Enabled {
			slog.Debug("backing: pvc create failed", "ns", b.namespace, "name", name, "err", err)
		}
		slog.Warn("backing: pvc create failed")
		return "", fmt.Errorf("backing: create pvc %s: %w", name, err)
	}
	if dbg.Enabled {
		slog.Debug("backing: pvc created, waiting for bind", "ns", b.namespace, "name", name)
	}
	return b.waitForBound(ctx, name)
}

func (b *backingProvisioner) DeleteBackingPVC(ctx context.Context, name string) error {
	err := b.cs.CoreV1().PersistentVolumeClaims(b.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("backing: delete pvc %s: %w", name, err)
	}
	return nil
}

func (b *backingProvisioner) waitForBound(ctx context.Context, name string) (string, error) {
	var pvName string
	start := time.Now()
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 5*time.Minute, true,
		func(ctx context.Context) (bool, error) {
			pvc, err := b.cs.CoreV1().PersistentVolumeClaims(b.namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			if dbg.Enabled {
				slog.Debug("backing: bind poll", "name", name, "phase", pvc.Status.Phase)
			}
			if pvc.Status.Phase == corev1.ClaimBound && pvc.Spec.VolumeName != "" {
				pvName = pvc.Spec.VolumeName
				return true, nil
			}
			return false, nil
		})
	if err != nil {
		slog.Warn("backing: pvc did not bind")
		return "", fmt.Errorf("backing: pvc %s did not bind: %w", name, err)
	}
	if dbg.Enabled {
		slog.Debug("backing: pvc bound", "name", name, "pv", pvName, "duration", time.Since(start).Round(time.Millisecond))
	}
	return pvName, nil
}
```

- [ ] **Step 2: Build and verify**

```shell
go build ./internal/app/
go build -tags debug ./internal/app/
```

Expected: no output, exit 0.

- [ ] **Step 3: Run existing tests**

```shell
go test ./internal/app/
```

Expected: `ok  github.com/nfcs/k8s-luks-csi-go/internal/app`

- [ ] **Step 4: Commit**

```shell
git add internal/app/backing.go
git commit -m "feat(backing): sanitised logs + build-time debug guards

Production: WARN with no PVC name or namespace on failure.
Debug: namespace, PVC name, storageClass, API error, bind poll phase,
PV name, and bind duration."
```

---

## Task 4: Instrument `internal/vault/rotate.go`

**Files:**
- Modify: `internal/vault/rotate.go`

`EnsureKey` and `DeleteKey` live here. Add sanitised WARN on failure and debug guards showing the vault path and whether the key was created vs already existed. Never log key bytes.

- [ ] **Step 1: Replace rotate.go with the instrumented version**

```go
package vault

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"

	"github.com/nfcs/k8s-luks-csi-go/internal/dbg"
	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
)

func (c *Client) FindAuthenticatingVersion(
	ctx context.Context, institution, volume string, tryOpen func([]byte) bool,
) (*keyzero.Buffer, int, error) {
	p, err := keyPath(institution, volume)
	if err != nil {
		return nil, 0, err
	}
	_, current, err := c.kv.Get(ctx, p)
	if err != nil {
		return nil, 0, fmt.Errorf("vault: rotate: read current: %w", err)
	}
	for v := current; v >= 1; v-- {
		raw, err := c.kv.GetVersion(ctx, p, v)
		if err != nil {
			continue
		}
		if tryOpen(raw) {
			buf, err := toBuffer(raw)
			if err != nil {
				return nil, 0, err
			}
			return buf, v, nil
		}
		for i := range raw {
			raw[i] = 0
		}
	}
	return nil, 0, fmt.Errorf("vault: no key version authenticates for %s/%s", institution, volume)
}

func (c *Client) EnsureKey(ctx context.Context, institution, volume string) error {
	p, err := keyPath(institution, volume)
	if err != nil {
		return err
	}
	if dbg.Enabled {
		slog.Debug("vault: ensure key", "path", mountPath+"/data/"+p)
	}
	if _, _, err := c.kv.Get(ctx, p); err == nil {
		if dbg.Enabled {
			slog.Debug("vault: key already exists")
		}
		return nil
	}
	key, err := c.generate(32)
	if err != nil {
		slog.Warn("vault: ensure key failed", "op", "generate")
		return fmt.Errorf("vault: generate key: %w", err)
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	if _, err := c.kv.Put(ctx, p, key); err != nil {
		slog.Warn("vault: ensure key failed", "op", "put")
		return fmt.Errorf("vault: ensure key: %w", err)
	}
	if dbg.Enabled {
		slog.Debug("vault: key created")
	}
	return nil
}

func (c *Client) DeleteKey(ctx context.Context, institution, volume string) error {
	p, err := keyPath(institution, volume)
	if err != nil {
		return err
	}
	if dbg.Enabled {
		slog.Debug("vault: delete key", "path", mountPath+"/data/"+p)
	}
	if err := c.kv.DeleteAll(ctx, p); err != nil {
		slog.Warn("vault: delete key failed")
		return err
	}
	if dbg.Enabled {
		slog.Debug("vault: key deleted")
	}
	return nil
}

func (c *Client) generate(n int) ([]byte, error) {
	if c.genKey != nil {
		return c.genKey(n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
```

- [ ] **Step 2: Build and verify**

```shell
go build ./internal/vault/
go build -tags debug ./internal/vault/
```

Expected: no output, exit 0.

- [ ] **Step 3: Run existing tests**

```shell
go test ./internal/vault/
```

Expected: `ok  github.com/nfcs/k8s-luks-csi-go/internal/vault`

- [ ] **Step 4: Commit**

```shell
git add internal/vault/rotate.go
git commit -m "feat(vault): sanitised logs + build-time debug guards in EnsureKey/DeleteKey

Production: WARN with op name only, no paths.
Debug: full vault path, created-vs-existed, delete confirmation."
```

---

## Task 5: Fix and instrument `internal/node/node.go`

**Files:**
- Modify: `internal/node/node.go`

Two existing `slog.Info` calls expose `institution`, `volume`, and `mapper` at INFO level — this violates the spec. Replace with sanitised INFO and move full detail into debug guards. Add debug guards for device resolution, format-vs-open decision, and LUKS UUID.

- [ ] **Step 1: Replace the import block**

The current import block is:
```go
import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"

	"github.com/nfcs/k8s-luks-csi-go/internal/csierr"
	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
	"github.com/nfcs/k8s-luks-csi-go/internal/validate"
)
```

Replace with:
```go
import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"

	"github.com/nfcs/k8s-luks-csi-go/internal/csierr"
	"github.com/nfcs/k8s-luks-csi-go/internal/dbg"
	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
	"github.com/nfcs/k8s-luks-csi-go/internal/validate"
)
```

- [ ] **Step 2: Replace NodeStageVolume**

Replace the existing `NodeStageVolume` function with:

```go
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
		slog.Warn("node stage failed", "op", "resolve device")
		return nil, csierr.Wrap(codes.Internal, "resolve device", err)
	}

	mapper, err := mapperName(institution, volume)
	if err != nil {
		return nil, csierr.Wrap(codes.Internal, "node stage", err)
	}

	if dbg.Enabled {
		slog.Debug("node stage: device resolved", "device", device, "mapper", mapper,
			"target", req.GetStagingTargetPath())
	}

	key, err := s.deps.Keys.KeyFor(ctx, Secret{RoleID: roleID, SecretID: secretID}, institution, volume)
	if err != nil {
		slog.Warn("node stage failed", "op", "fetch key")
		return nil, csierr.Wrap(codes.Internal, "fetch key", err)
	}
	defer key.Zero()

	isLUKS, err := s.deps.Luks.IsLUKS(ctx, device)
	if err != nil {
		slog.Warn("node stage failed", "op", "probe luks")
		return nil, csierr.Wrap(codes.Internal, "probe luks", err)
	}

	if !isLUKS {
		if dbg.Enabled {
			slog.Debug("node stage: new volume — format + open", "mapper", mapper)
		}
		if err := s.deps.Luks.Format(ctx, device, key); err != nil {
			slog.Warn("node stage failed", "op", "format")
			return nil, csierr.Wrap(codes.Internal, "format", err)
		}
		if err := s.deps.Luks.Open(ctx, device, mapper, key); err != nil {
			slog.Warn("node stage failed", "op", "open after format")
			return nil, csierr.Wrap(codes.Internal, "open", err)
		}
		if err := s.deps.Luks.MakeFS(ctx, mapper); err != nil {
			slog.Warn("node stage failed", "op", "mkfs")
			return nil, csierr.Wrap(codes.Internal, "mkfs", err)
		}
	} else {
		if dbg.Enabled {
			slog.Debug("node stage: existing volume — open only", "mapper", mapper)
		}
		if err := s.deps.Luks.Open(ctx, device, mapper, key); err != nil {
			slog.Warn("node stage failed", "op", "open")
			return nil, csierr.Wrap(codes.Internal, "open", err)
		}
	}

	if err := s.deps.Mount.Mount(ctx, "/dev/mapper/"+mapper, req.GetStagingTargetPath()); err != nil {
		slog.Warn("node stage failed", "op", "mount")
		return nil, csierr.Wrap(codes.Internal, "mount", err)
	}

	if dbg.Enabled {
		slog.Debug("node stage: ok", "device", device, "mapper", mapper,
			"target", req.GetStagingTargetPath(), "new_volume", !isLUKS)
	}
	slog.Info("volume staged")
	return &csi.NodeStageVolumeResponse{}, nil
}
```

- [ ] **Step 3: Replace NodeUnstageVolume**

Replace the existing `NodeUnstageVolume` function with:

```go
func (s *Service) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "node unstage", fmt.Errorf("missing volume id"))
	}
	if req.GetStagingTargetPath() == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "node unstage", fmt.Errorf("missing staging target path"))
	}
	institution, volume, _, err := parseVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, csierr.Wrap(codes.InvalidArgument, "node unstage", err)
	}
	mapper, err := mapperName(institution, volume)
	if err != nil {
		return nil, csierr.Wrap(codes.Internal, "node unstage", err)
	}
	if dbg.Enabled {
		slog.Debug("node unstage", "mapper", mapper, "target", req.GetStagingTargetPath())
	}
	if err := s.deps.Mount.Unmount(ctx, req.GetStagingTargetPath()); err != nil {
		slog.Warn("node unstage failed", "op", "unmount")
		return nil, csierr.Wrap(codes.Internal, "unmount staging", err)
	}
	if err := s.deps.Luks.Close(ctx, mapper); err != nil {
		slog.Warn("node unstage failed", "op", "luks close")
		return nil, csierr.Wrap(codes.Internal, "luks close", err)
	}
	if dbg.Enabled {
		slog.Debug("node unstage: ok", "mapper", mapper)
	}
	slog.Info("volume unstaged")
	return &csi.NodeUnstageVolumeResponse{}, nil
}
```

- [ ] **Step 4: Build and verify**

```shell
go build ./internal/node/
go build -tags debug ./internal/node/
```

Expected: no output, exit 0.

- [ ] **Step 5: Run existing tests**

```shell
go test ./internal/node/
```

Expected: `ok  github.com/nfcs/k8s-luks-csi-go/internal/node`

- [ ] **Step 6: Commit**

```shell
git add internal/node/node.go
git commit -m "feat(node): sanitised logs + build-time debug guards in NodeStageVolume

Remove identifier-leaking slog.Info calls (institution, volume, mapper at INFO).
Production: sanitised WARN per failing op, sanitised INFO on success.
Debug: device path, mapper name, staging target, format-vs-open decision."
```

---

## Task 6: Update Dockerfile

**Files:**
- Modify: `Dockerfile`

Add `ARG DEBUG_BUILD=false` to the build stage and branch the `go build` command on it.

- [ ] **Step 1: Replace the build stage RUN command**

The current build RUN in the `build` stage is:
```dockerfile
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/luks-csi ./cmd/luks-csi
```

Replace the entire build stage with:
```dockerfile
# --- build stage ---
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG DEBUG_BUILD=false
RUN if [ "$DEBUG_BUILD" = "true" ]; then \
      CGO_ENABLED=0 go build -tags debug -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/luks-csi ./cmd/luks-csi; \
    else \
      CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/luks-csi ./cmd/luks-csi; \
    fi
```

- [ ] **Step 2: Verify the Dockerfile parses correctly**

```shell
podman build --dry-run -f Dockerfile \
  /Users/hd25646/Documents/NFCS-secure-k8s/k8s-luks-csi-go/.claude/worktrees/feat+go-luks-csi-driver \
  2>&1 | head -5
```

Expected: no parse errors.

- [ ] **Step 3: Commit**

```shell
git add Dockerfile
git commit -m "feat(dockerfile): add DEBUG_BUILD arg to build stage

Pass --build-arg DEBUG_BUILD=true to produce a debug binary.
Defaults to false (production build) when arg is omitted."
```

---

## Task 7: Acceptance criteria verification

- [ ] **Step 1: Full build — verify no debug strings in production binary**

```shell
cd /Users/hd25646/Documents/NFCS-secure-k8s/k8s-luks-csi-go/.claude/worktrees/feat+go-luks-csi-driver
go build -o /tmp/luks-csi-prod ./cmd/luks-csi
strings /tmp/luks-csi-prod | grep -c "node stage: device resolved"
```

Expected output: `0`

- [ ] **Step 2: Debug build — verify debug strings are present**

```shell
go build -tags debug -o /tmp/luks-csi-debug ./cmd/luks-csi
strings /tmp/luks-csi-debug | grep -c "node stage: device resolved"
```

Expected output: `1` (or more)

- [ ] **Step 3: Verify no identifiers in production logs at INFO/WARN/ERROR level**

```shell
grep -rn 'slog\.\(Info\|Warn\|Error\)' \
  internal/controller/controller.go \
  internal/app/backing.go \
  internal/vault/rotate.go \
  internal/node/node.go
```

Manually confirm none of the matched lines contain: institution names, PVC names, Vault paths, device paths, or mapper names as literal arguments (i.e. no `"institution", institution` or `"device", device` outside of a `if dbg.Enabled` block).

- [ ] **Step 4: Run all tests in both build variants**

```shell
go test ./...
go test -tags debug ./...
```

Expected: all packages pass in both variants.

- [ ] **Step 5: Final commit**

```shell
git add .
git commit -m "test: verify debug logging acceptance criteria

Production binary: 0 debug strings.
Debug binary: debug strings present.
All tests pass in both build variants."
```
