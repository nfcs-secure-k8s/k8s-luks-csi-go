package node

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

// Secret is the per-tenant Vault credential from node-stage secrets.
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
	Close(ctx context.Context, name string) error
}

// DeviceOps resolves+attaches the backing device.
type DeviceOps interface {
	ResolveAndAttach(ctx context.Context, volumeContext map[string]string) (string, error)
}

// Mounter mounts/unmounts the opened mapper at the staging and target paths.
type Mounter interface {
	Mount(ctx context.Context, source, target string) error
	BindMount(ctx context.Context, source, target string) error
	Unmount(ctx context.Context, target string) error
}

// Stager manages staging pods that trigger the backing CSI driver's
// NodeStageVolume, making the backing block device visible on the host.
type Stager interface {
	EnsureStaged(ctx context.Context, volumeID string, volumeContext map[string]string) error
	ReleaseStaged(ctx context.Context, volumeID string) error
	ReconcileOrphans(ctx context.Context) error
}

// Deps are the node service's collaborators.
type Deps struct {
	Keys   KeySource
	Luks   LUKSOps
	Device DeviceOps
	Mount  Mounter
	Stager Stager // nil is valid for tests that don't exercise staging
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

	if s.deps.Stager != nil {
		if err := s.deps.Stager.EnsureStaged(ctx, req.GetVolumeId(), req.GetVolumeContext()); err != nil {
			slog.Warn("node stage failed", "op", "ensure staged")
			return nil, csierr.Wrap(codes.Internal, "ensure staged", err)
		}
	}

	device, err := s.deps.Device.ResolveAndAttach(ctx, req.GetVolumeContext())
	if err != nil {
		if dbg.Enabled {
			slog.Debug("node stage resolve error", "err", err)
		}
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
	} else {
		if dbg.Enabled {
			slog.Debug("node stage: existing volume — open only", "mapper", mapper)
		}
		if err := s.deps.Luks.Open(ctx, device, mapper, key); err != nil {
			slog.Warn("node stage failed", "op", "open")
			return nil, csierr.Wrap(codes.Internal, "open", err)
		}
	}

	// MakeFS is idempotent (skips if filesystem already present). Runs in both
	// branches to handle volumes that were formatted but not yet mkfs'd.
	if err := s.deps.Luks.MakeFS(ctx, mapper); err != nil {
		if dbg.Enabled {
			slog.Debug("node stage mkfs error", "err", err)
		}
		slog.Warn("node stage failed", "op", "mkfs")
		return nil, csierr.Wrap(codes.Internal, "mkfs", err)
	}

	if err := s.deps.Mount.Mount(ctx, "/dev/mapper/"+mapper, req.GetStagingTargetPath()); err != nil {
		if dbg.Enabled {
			slog.Debug("node stage mount error", "err", err)
		}
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
	// Release the staging pod only after the LUKS mapper is closed. The pod
	// holds the backing block device mapped on the host; deleting it triggers
	// the backing driver's unmap. Doing so while the dm-crypt mapper is still
	// open would pull the device out from under an active crypt layer. If Close
	// fails above we deliberately leave the pod in place and let kubelet retry
	// NodeUnstageVolume — a stuck volume is preferable to an unsafe unmap.
	if s.deps.Stager != nil {
		if err := s.deps.Stager.ReleaseStaged(ctx, req.GetVolumeId()); err != nil {
			slog.Warn("node unstage failed", "op", "release staged")
			return nil, csierr.Wrap(codes.Internal, "release staged", err)
		}
	}
	if dbg.Enabled {
		slog.Debug("node unstage: ok", "mapper", mapper)
	}
	slog.Info("volume unstaged")
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (s *Service) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "node publish", fmt.Errorf("missing volume id"))
	}
	if req.GetTargetPath() == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "node publish", fmt.Errorf("missing target path"))
	}
	if req.GetVolumeCapability() == nil {
		return nil, csierr.Wrap(codes.InvalidArgument, "node publish", fmt.Errorf("missing volume capability"))
	}
	// CSI spec: SP MUST create the target path directory as part of NodePublishVolume.
	if err := os.MkdirAll(req.GetTargetPath(), 0o750); err != nil {
		return nil, csierr.Wrap(codes.Internal, "create target path", err)
	}
	// M-3: device-specific bind mount only; staging→target bind.
	if err := s.deps.Mount.BindMount(ctx, req.GetStagingTargetPath(), req.GetTargetPath()); err != nil {
		return nil, csierr.Wrap(codes.Internal, "bind mount", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func (s *Service) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "node unpublish", fmt.Errorf("missing volume id"))
	}
	if req.GetTargetPath() == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "node unpublish", fmt.Errorf("missing target path"))
	}
	if err := s.deps.Mount.Unmount(ctx, req.GetTargetPath()); err != nil {
		return nil, csierr.Wrap(codes.Internal, "unmount target", err)
	}
	_ = os.Remove(req.GetTargetPath()) // best-effort; idempotent on ENOENT
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (s *Service) NodeGetCapabilities(_ context.Context, _ *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{
				Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
			}}},
		},
	}, nil
}

func (s *Service) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	nodeID := os.Getenv("KUBE_NODE_NAME")
	if nodeID == "" {
		nodeID = "unknown"
	}
	return &csi.NodeGetInfoResponse{NodeId: nodeID}, nil
}

// Reconcile runs startup orphan reconciliation. Call once after the service
// is wired, before serving requests.
func (s *Service) Reconcile(ctx context.Context) error {
	if s.deps.Stager == nil {
		return nil
	}
	return s.deps.Stager.ReconcileOrphans(ctx)
}

// mapperName derives the validated /dev/mapper target (H-5).
func mapperName(institution, volume string) (string, error) {
	name := "luks-" + sha256hex16(institution+"\x00"+volume)
	if err := validate.MapperName(name); err != nil {
		return "", err
	}
	return name, nil
}

// parseVolumeID parses "<institution>/<volume>/<policy>".
func parseVolumeID(id string) (institution, volume, policy string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("malformed volume id %q", id)
	}
	return parts[0], parts[1], parts[2], nil
}
