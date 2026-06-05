package device

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// AttachRequest describes an attach-based backend attachment.
type AttachRequest struct {
	PVName   string
	NodeName string
	Attacher string // CSI driver name of the backing attacher
}

// Attacher drives VolumeAttachment lifecycle.
type Attacher struct {
	cs   kubernetes.Interface
	poll time.Duration
	wait time.Duration
}

func NewAttacher(cs kubernetes.Interface) *Attacher {
	return &Attacher{cs: cs, poll: 500 * time.Millisecond, wait: 2 * time.Minute}
}

// vaName derives a deterministic VolumeAttachment name from (pv, node).
func vaName(pv, node string) string {
	sum := sha256.Sum256([]byte(pv + "\x00" + node))
	return "luks-va-" + hex.EncodeToString(sum[:8])
}

// Attach creates the VolumeAttachment (idempotently) and waits until
// Status.Attached is true (H-3: no node pod-create needed).
func (a *Attacher) Attach(ctx context.Context, req AttachRequest) error {
	name := vaName(req.PVName, req.NodeName)
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: req.Attacher,
			NodeName: req.NodeName,
			Source:   storagev1.VolumeAttachmentSource{PersistentVolumeName: &req.PVName},
		},
	}
	_, err := a.cs.StorageV1().VolumeAttachments().Create(ctx, va, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("device: create VolumeAttachment: %w", err)
	}
	return wait.PollUntilContextTimeout(ctx, a.poll, a.wait, true,
		func(ctx context.Context) (bool, error) {
			cur, err := a.cs.StorageV1().VolumeAttachments().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			if cur.Status.AttachError != nil {
				return false, fmt.Errorf("device: attach error: %s", cur.Status.AttachError.Message)
			}
			return cur.Status.Attached, nil
		})
}

// Detach deletes the VolumeAttachment. Idempotent: NotFound is success.
func (a *Attacher) Detach(ctx context.Context, pvName, nodeName string) error {
	err := a.cs.StorageV1().VolumeAttachments().Delete(ctx, vaName(pvName, nodeName), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("device: delete VolumeAttachment: %w", err)
	}
	return nil
}
