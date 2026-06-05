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
		VolumeContext:     map[string]string{"institution": "bristol"},
		// no secrets => InvalidArgument (H-1: node has no standing creds)
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestNodeStageFormatsWhenNotLUKSAndZeroesKey(t *testing.T) {
	fk := &fakeKeySource{}
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
		VolumeContext:     map[string]string{"institution": "bristol"},
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
	// Key must be zeroed after staging (H-4/M-4).
	// Use rawData (captured before Zero) because Bytes() panics post-Zero.
	if fk.rawData != nil && !allZero(fk.rawData) {
		t.Fatal("key buffer was not zeroed after staging")
	}
}

func TestNodeStageOpensExistingLUKSWithoutFormat(t *testing.T) {
	fk := &fakeKeySource{}
	fl := &fakeLUKS{isLUKS: true}
	s := New(Deps{Keys: fk, Luks: fl, Device: &fakeDevice{path: "/dev/x"}, Mount: &fakeMounter{}})
	_, err := s.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "bristol/pvc-1/Retain",
		StagingTargetPath: "/stage",
		VolumeContext:     map[string]string{"institution": "bristol"},
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

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// --- fakes ---

type fakeKeySource struct {
	buf     *keyzero.Buffer
	rawData []byte // captured before Zero so allZero can be checked post-call
}

func (f *fakeKeySource) KeyFor(_ context.Context, _ Secret, _, _ string) (*keyzero.Buffer, error) {
	b, _ := keyzero.NewBuffer(32)
	f.buf = b
	f.rawData = b.Bytes() // capture slice reference; valid until Zero is called
	return b, nil
}

type fakeLUKS struct {
	isLUKS            bool
	formatted, opened bool
	madeFS            bool
}

func (f *fakeLUKS) IsLUKS(_ context.Context, _ string) (bool, error) { return f.isLUKS, nil }
func (f *fakeLUKS) Format(_ context.Context, _ string, _ *keyzero.Buffer) error {
	f.formatted = true
	return nil
}
func (f *fakeLUKS) Open(_ context.Context, _, _ string, _ *keyzero.Buffer) error {
	f.opened = true
	return nil
}
func (f *fakeLUKS) MakeFS(_ context.Context, _ string) error { f.madeFS = true; return nil }

type fakeDevice struct{ path string }

func (f *fakeDevice) ResolveAndAttach(_ context.Context, _ map[string]string) (string, error) {
	return f.path, nil
}

type fakeMounter struct{}

func (f *fakeMounter) Mount(_ context.Context, _, _ string) error { return nil }
