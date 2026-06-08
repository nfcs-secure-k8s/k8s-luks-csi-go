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
		policy = "Retain" // M-6: default is Retain, not Delete
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
	volumeCtx := map[string]string{
		"backingPV":      pvName,
		"institution":    institution,
		"luksType":       "luks2",
		"deletionPolicy": policy,
	}
	// Pass through resolver hints (e.g. devicePath+backend=direct for loop-device tests).
	for _, k := range []string{"devicePath", "backend"} {
		if v := params[k]; v != "" {
			volumeCtx[k] = v
		}
	}
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volID,
			CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
			VolumeContext: volumeCtx,
		},
	}, nil
}

func (s *Service) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "delete volume", fmt.Errorf("missing volume id"))
	}
	institution, volume, policy, err := parseVolumeID(req.GetVolumeId())
	if err != nil {
		// CSI spec §6.3: return success for volumes that were not created by this plugin
		// or that do not exist — DeleteVolume must be idempotent.
		return &csi.DeleteVolumeResponse{}, nil
	}
	switch policy {
	case "Delete":
		if err := s.deps.Vault.DeleteKey(ctx, institution, volume); err != nil {
			return nil, csierr.Wrap(codes.Internal, "delete key", err)
		}
		if err := s.deps.Backing.DeleteBackingPVC(ctx, backingName(institution, volume)); err != nil {
			return nil, csierr.Wrap(codes.Internal, "delete backing pvc", err)
		}
	case "Retain":
		// no-op: key and backing PVC are preserved (M-6 default)
	default:
		return nil, csierr.Wrap(codes.InvalidArgument, "delete volume",
			fmt.Errorf("unknown deletionPolicy %q", policy))
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (s *Service) ControllerGetCapabilities(_ context.Context, _ *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	caps := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
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

func (s *Service) ValidateVolumeCapabilities(_ context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, csierr.Wrap(codes.InvalidArgument, "validate volume capabilities", fmt.Errorf("missing volume id"))
	}
	if len(req.GetVolumeCapabilities()) == 0 {
		return nil, csierr.Wrap(codes.InvalidArgument, "validate volume capabilities", fmt.Errorf("missing volume capabilities"))
	}
	// Volumes created by this driver have format "institution/volume/policy".
	// Any other ID format means the volume was not created here → NotFound.
	if _, _, _, err := parseVolumeID(req.GetVolumeId()); err != nil {
		return nil, csierr.Wrap(codes.NotFound, "validate volume capabilities", fmt.Errorf("volume %q not found", req.GetVolumeId()))
	}
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.GetVolumeCapabilities(),
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
