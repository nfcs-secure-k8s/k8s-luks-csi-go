package device

import (
	"context"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestAttachCreatesVolumeAttachmentAndWaitsAttached(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "volumeattachments",
		func(a k8stesting.Action) (bool, runtime.Object, error) {
			va := a.(k8stesting.CreateAction).GetObject().(*storagev1.VolumeAttachment)
			va.Status.Attached = true
			return false, va, nil
		})

	a := NewAttacher(cs)
	_, err := a.Attach(context.Background(), AttachRequest{
		PVName: "pv-1", NodeName: "node-a", Attacher: "luks.csi.nfcs.io",
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	vas, _ := cs.StorageV1().VolumeAttachments().List(context.Background(), metav1.ListOptions{})
	if len(vas.Items) != 1 {
		t.Fatalf("expected 1 VolumeAttachment, got %d", len(vas.Items))
	}
}

func TestAttachReturnsAttachmentMetadata(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "volumeattachments",
		func(a k8stesting.Action) (bool, runtime.Object, error) {
			va := a.(k8stesting.CreateAction).GetObject().(*storagev1.VolumeAttachment)
			va.Status.Attached = true
			va.Status.AttachmentMetadata = map[string]string{"devicePath": "/dev/rbd0"}
			return false, va, nil
		})

	a := NewAttacher(cs)
	meta, err := a.Attach(context.Background(), AttachRequest{
		PVName: "pv-1", NodeName: "node-a", Attacher: "rbd.csi.ceph.com",
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if meta["devicePath"] != "/dev/rbd0" {
		t.Fatalf("meta[devicePath] = %q, want /dev/rbd0", meta["devicePath"])
	}
}

func TestDetachRemovesVolumeAttachment(t *testing.T) {
	existing := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: vaName("pv-1", "node-a")},
	}
	cs := fake.NewSimpleClientset(existing)
	a := NewAttacher(cs)
	if err := a.Detach(context.Background(), "pv-1", "node-a"); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	vas, _ := cs.StorageV1().VolumeAttachments().List(context.Background(), metav1.ListOptions{})
	if len(vas.Items) != 0 {
		t.Fatalf("expected 0 VolumeAttachments after detach, got %d", len(vas.Items))
	}
}
