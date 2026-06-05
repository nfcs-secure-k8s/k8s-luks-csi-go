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
}

// DeviceOps resolves+attaches the backing device.
type DeviceOps interface {
	ResolveAndAttach(ctx context.Context, volumeContext map[string]string) (string, error)
}

// Mounter mounts the opened mapper at the staging path.
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
	defer key.Zero() // H-4/M-4: zeroed on every return path

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
			return nil, csierr.Wrap(codes.Internal, "open", err)
		}
	}

	if err := s.deps.Mount.Mount(ctx, "/dev/mapper/"+mapper, req.GetStagingTargetPath()); err != nil {
		return nil, csierr.Wrap(codes.Internal, "mount", err)
	}
	// H-7: log institution and mapper name, never key material or key-file paths.
	slog.Info("staged volume", "institution", institution, "volume", volume, "mapper", mapper)
	return &csi.NodeStageVolumeResponse{}, nil
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
