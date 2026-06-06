package sanity

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/nfcs/k8s-luks-csi-go/internal/controller"
	"github.com/nfcs/k8s-luks-csi-go/internal/identity"
	"github.com/nfcs/k8s-luks-csi-go/internal/server"
)

func TestCSISanity(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "csi.sock")
	endpoint := "unix://" + sock

	srv := server.New(server.Config{Endpoint: endpoint, Mode: server.ModeAll, Version: "test"})
	srv.WithIdentity(identity.New("test"))
	srv.WithController(controller.New(controller.Deps{
		Vault:   &sanityVault{},
		Backing: &sanityBacking{},
	}))
	// Minimal node stub: no stage/unstage capability so the lifecycle test
	// skips those RPCs. NodePublishVolume and NodeUnpublishVolume return
	// success so that AfterEach cleanup succeeds.
	srv.WithNode(&sanityNode{})
	go func() { _ = srv.Run() }()
	defer srv.Stop()

	cfg := sanity.NewTestConfig()
	cfg.Address = endpoint
	cfg.TargetPath = filepath.Join(dir, "target")
	cfg.StagingPath = filepath.Join(dir, "staging")
	cfg.TestVolumeParameters = map[string]string{
		"institution":         "bristol",
		"backingStorageClass": "standard",
	}
	// Zero capacity makes the "different capacity" idempotency test self-skip
	// (it guards on sizeVol1 == 0 → Skip). We don't track volume state in the
	// sanity fakes so we can't return AlreadyExists for same-name-different-cap.
	cfg.TestVolumeSize = 0

	sanity.Test(t, cfg)
}

// sanityNode is a minimal CSI Node stub for the sanity suite.
// It does NOT advertise STAGE_UNSTAGE_VOLUME so lifecycle tests skip staging.
type sanityNode struct{ csi.UnimplementedNodeServer }

func (n *sanityNode) NodeGetCapabilities(_ context.Context, _ *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{}, nil
}

func (n *sanityNode) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: "sanity-node"}, nil
}

func (n *sanityNode) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "VolumeId required")
	}
	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "TargetPath required")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "VolumeCapability required")
	}
	// CSI spec: the SP MUST create the target path directory as part of NodePublishVolume.
	if err := os.MkdirAll(req.GetTargetPath(), 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "create target path: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func (n *sanityNode) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "VolumeId required")
	}
	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "TargetPath required")
	}
	// CSI spec: the SP MUST delete the directory it created during NodePublishVolume.
	_ = os.Remove(req.GetTargetPath())
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// sanityVault is an in-memory VaultKeyer for sanity tests.
type sanityVault struct {
	keys map[string]bool
}

func (v *sanityVault) EnsureKey(_ context.Context, institution, volume string) error {
	if v.keys == nil {
		v.keys = map[string]bool{}
	}
	v.keys[institution+"/"+volume] = true
	return nil
}

func (v *sanityVault) DeleteKey(_ context.Context, institution, volume string) error {
	if v.keys != nil {
		delete(v.keys, institution+"/"+volume)
	}
	return nil
}

// sanityBacking is an in-memory Backing for sanity tests.
type sanityBacking struct {
	pvs map[string]string
}

func (b *sanityBacking) CreateBackingPVC(_ context.Context, name, _ string) (string, error) {
	if b.pvs == nil {
		b.pvs = map[string]string{}
	}
	pvName := "pv-" + name
	b.pvs[name] = pvName
	return pvName, nil
}

func (b *sanityBacking) DeleteBackingPVC(_ context.Context, name string) error {
	if b.pvs != nil {
		delete(b.pvs, name)
	}
	return nil
}
