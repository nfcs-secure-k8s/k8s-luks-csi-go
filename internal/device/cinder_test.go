package device

import (
	"context"
	"strings"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCinderResolverHappyPath(t *testing.T) {
	cs := fakeAttachedCSWithMeta(map[string]string{"DevicePath": "/dev/vdb"})
	r := NewCinderResolver(cs, "node-1")
	path, err := r.Resolve(context.Background(), map[string]string{
		"backingPV": "pvc-7addc49f-e822-4465-a416-284d924dab4d",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/dev/vdb" {
		t.Fatalf("path = %q, want /dev/vdb", path)
	}
}

func TestCinderResolverUsesCinderAttacher(t *testing.T) {
	var gotAttacher string
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "volumeattachments",
		func(a k8stesting.Action) (bool, runtime.Object, error) {
			va := a.(k8stesting.CreateAction).GetObject().(*storagev1.VolumeAttachment)
			gotAttacher = va.Spec.Attacher
			va.Status.Attached = true
			va.Status.AttachmentMetadata = map[string]string{"DevicePath": "/dev/vdc"}
			return false, va, nil
		})

	r := NewCinderResolver(cs, "node-1")
	if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": "pvc-abc"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotAttacher != "cinder.csi.openstack.org" {
		t.Fatalf("attacher = %q, want cinder.csi.openstack.org", gotAttacher)
	}
}

func TestCinderResolverMissingDevicePath(t *testing.T) {
	cs := fakeAttachedCSWithMeta(map[string]string{})
	r := NewCinderResolver(cs, "node-1")
	if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": "pvc-abc"}); err == nil {
		t.Fatal("expected error when DevicePath is absent from metadata")
	}
}

func TestCinderResolverRejectsInvalidBackingPV(t *testing.T) {
	r := NewCinderResolver(fake.NewSimpleClientset(), "node-1")
	bad := []string{
		"",
		"../../etc/passwd",
		"pvc/bad",
		"pvc..bad",
		"PVC-UPPERCASE",
		"pvc-" + strings.Repeat("a", 260),
	}
	for _, name := range bad {
		if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": name}); err == nil {
			t.Fatalf("expected rejection for backingPV %q", name)
		}
	}
}
