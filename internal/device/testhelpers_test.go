package device

import (
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeAttachedCS returns a clientset whose VolumeAttachment create reactor
// immediately sets Attached=true with no metadata.
func fakeAttachedCS() *fake.Clientset {
	return fakeAttachedCSWithMeta(nil)
}

// fakeAttachedCSWithMeta returns a clientset whose VolumeAttachment create
// reactor immediately sets Attached=true and populates AttachmentMetadata.
func fakeAttachedCSWithMeta(meta map[string]string) *fake.Clientset {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "volumeattachments",
		func(a k8stesting.Action) (bool, runtime.Object, error) {
			va := a.(k8stesting.CreateAction).GetObject().(*storagev1.VolumeAttachment)
			va.Status.Attached = true
			va.Status.AttachmentMetadata = meta
			return false, va, nil
		})
	return cs
}
