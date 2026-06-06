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
		VolumeCapabilities: []*csi.VolumeCapability{
			{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}},
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

func TestDeleteVolumeUnknownPolicyReturnsInvalidArgument(t *testing.T) {
	c := New(Deps{Vault: &fakeVault{}, Backing: &fakeBacking{}})
	_, err := c.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{
		VolumeId: "bristol/pvc-abc/Unknown",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument for unknown policy", status.Code(err))
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
