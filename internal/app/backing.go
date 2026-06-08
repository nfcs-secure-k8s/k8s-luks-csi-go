package app

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// backingProvisioner creates and removes backing block PVCs via client-go.
type backingProvisioner struct {
	cs        kubernetes.Interface
	namespace string
}

func (b *backingProvisioner) CreateBackingPVC(ctx context.Context, name, storageClass string) (string, error) {
	blockMode := corev1.PersistentVolumeBlock
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: b.namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &storageClass,
			VolumeMode:       &blockMode,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
	_, err := b.cs.CoreV1().PersistentVolumeClaims(b.namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("backing: create pvc %s: %w", name, err)
	}
	return b.waitForBound(ctx, name)
}

func (b *backingProvisioner) DeleteBackingPVC(ctx context.Context, name string) error {
	err := b.cs.CoreV1().PersistentVolumeClaims(b.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("backing: delete pvc %s: %w", name, err)
	}
	return nil
}

func (b *backingProvisioner) waitForBound(ctx context.Context, name string) (string, error) {
	var pvName string
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 5*time.Minute, true,
		func(ctx context.Context) (bool, error) {
			pvc, err := b.cs.CoreV1().PersistentVolumeClaims(b.namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			if pvc.Status.Phase == corev1.ClaimBound && pvc.Spec.VolumeName != "" {
				pvName = pvc.Spec.VolumeName
				return true, nil
			}
			return false, nil
		})
	if err != nil {
		return "", fmt.Errorf("backing: pvc %s did not bind: %w", name, err)
	}
	return pvName, nil
}
