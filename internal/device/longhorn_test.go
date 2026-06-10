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

func TestLonghornResolverHappyPath(t *testing.T) {
	r := NewLonghornResolver(fakeAttachedCS(), "node-1")
	path, err := r.Resolve(context.Background(), map[string]string{
		"backingPV": "pvc-7addc49f-e822-4465-a416-284d924dab4d",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	const want = "/dev/longhorn/pvc-7addc49f-e822-4465-a416-284d924dab4d"
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestLonghornResolverUsesLonghornAttacher(t *testing.T) {
	var gotAttacher string
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "volumeattachments",
		func(a k8stesting.Action) (bool, runtime.Object, error) {
			va := a.(k8stesting.CreateAction).GetObject().(*storagev1.VolumeAttachment)
			gotAttacher = va.Spec.Attacher
			va.Status.Attached = true
			return false, va, nil
		})

	r := NewLonghornResolver(cs, "node-1")
	if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": "pvc-abc"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotAttacher != "driver.longhorn.io" {
		t.Fatalf("attacher = %q, want driver.longhorn.io", gotAttacher)
	}
}

func TestLonghornResolverRejectsInvalidBackingPV(t *testing.T) {
	r := NewLonghornResolver(fake.NewSimpleClientset(), "node-1")
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
