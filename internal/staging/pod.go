// Package staging manages short-lived pause pods in the luks-csi-staging
// namespace that trigger a backing CSI driver's NodeStageVolume by mounting
// the backing PVC. Once the pod is Running the backing block device is
// visible on the host node.
package staging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultPauseImage = "registry.k8s.io/pause:3.9"
	defaultTimeout    = 5 * time.Minute
	pollInterval      = 500 * time.Millisecond
	managedByLabel    = "app.kubernetes.io/managed-by"
	managedByValue    = "luks-csi-driver"
	componentLabel    = "app.kubernetes.io/component"
	volumeHashAnnot   = "luks.csi.nfcs.io/volume-hash"
)

// Config holds Manager configuration.
type Config struct {
	NodeName   string
	StagingNS  string        // namespace where staging pods are created
	BackingNS  string        // namespace where backing PVCs live
	PauseImage string        // image for the pause container (default: registry.k8s.io/pause:3.9)
	Timeout    time.Duration // how long to wait for a pod to become Running (default: 5m)
}

// Manager creates and manages staging pods.
type Manager struct {
	cs  kubernetes.Interface
	cfg Config
}

// New returns a Manager. PauseImage and Timeout are defaulted if zero.
func New(cs kubernetes.Interface, cfg Config) *Manager {
	if cfg.PauseImage == "" {
		cfg.PauseImage = defaultPauseImage
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Manager{cs: cs, cfg: cfg}
}

// EnsureStaged idempotently creates a staging pod for the given volumeID and
// waits until it is Running, returning the pod UID (kubelet keys the published
// backing device by it). Stale Failed/Succeeded pods are deleted and
// recreated. The backingPVCName key must be present in vc.
func (m *Manager) EnsureStaged(ctx context.Context, volumeID string, vc map[string]string) (string, error) {
	podName := stagingPodName(volumeID)

	existing, err := m.cs.CoreV1().Pods(m.cfg.StagingNS).Get(ctx, podName, metav1.GetOptions{})
	if err == nil {
		switch existing.Status.Phase {
		case corev1.PodRunning:
			return string(existing.UID), nil // already staged
		case corev1.PodFailed, corev1.PodSucceeded:
			slog.Info("staging: deleting stale terminal pod", "pod", podName, "phase", existing.Status.Phase)
			if err := m.cs.CoreV1().Pods(m.cfg.StagingNS).Delete(ctx, podName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return "", fmt.Errorf("staging: delete stale pod: %w", err)
			}
			// The deleted pod can linger (DeletionTimestamp set) while terminating.
			// Wait for it to be fully gone before Create, otherwise Create returns
			// AlreadyExists and waitRunning reads the still-terminating Failed pod.
			if err := m.waitDeleted(ctx, podName); err != nil {
				return "", err
			}
		}
		// Pending/Unknown — fall through to Create (idempotent) then wait.
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("staging: get pod: %w", err)
	}

	backingPVC := vc["backingPVCName"]
	if backingPVC == "" {
		return "", fmt.Errorf("staging: backingPVCName missing from volume context")
	}

	node, err := m.cs.CoreV1().Nodes().Get(ctx, m.cfg.NodeName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("staging: get node: %w", err)
	}

	// Existence pre-check: fail fast if the backing PVC is absent rather than
	// letting the staging pod hang Pending until the staging timeout.
	if _, err := m.cs.CoreV1().PersistentVolumeClaims(m.cfg.BackingNS).Get(ctx, backingPVC, metav1.GetOptions{}); err != nil {
		return "", fmt.Errorf("staging: get backing pvc: %w", err)
	}

	pod := buildPod(podName, m.cfg.StagingNS, backingPVC, m.cfg.NodeName, m.cfg.PauseImage, volumeID, node.Spec.Taints)
	if _, err := m.cs.CoreV1().Pods(m.cfg.StagingNS).Create(ctx, pod, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("staging: create pod: %w", err)
	}

	return m.waitRunning(ctx, podName)
}

// ReleaseStaged deletes the staging pod for volumeID. Idempotent: NotFound is success.
func (m *Manager) ReleaseStaged(ctx context.Context, volumeID string) error {
	err := m.cs.CoreV1().Pods(m.cfg.StagingNS).Delete(ctx, stagingPodName(volumeID), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("staging: delete pod: %w", err)
	}
	return nil
}

// ReconcileOrphans lists all staging pods owned by this driver and deletes any
// that are in a terminal (Failed or Succeeded) phase. Call once at driver startup.
func (m *Manager) ReconcileOrphans(ctx context.Context) error {
	pods, err := m.cs.CoreV1().Pods(m.cfg.StagingNS).List(ctx, metav1.ListOptions{
		LabelSelector: managedByLabel + "=" + managedByValue,
	})
	if err != nil {
		return fmt.Errorf("staging: reconcile: list pods: %w", err)
	}
	for _, pod := range pods.Items {
		// DaemonSet: one Manager per node sharing StagingNS. Only reconcile pods
		// scheduled to this node so we don't delete other nodes' staging pods.
		if pod.Spec.NodeName != m.cfg.NodeName {
			continue
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			if err := m.cs.CoreV1().Pods(m.cfg.StagingNS).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				slog.Warn("staging: reconcile: could not delete orphan", "pod", pod.Name, "err", err)
				continue
			}
			slog.Info("staging: reconcile: deleted orphan pod", "pod", pod.Name)
		}
	}
	return nil
}

// waitRunning polls until the named pod reaches Running phase or the context
// deadline, and returns the Running pod's UID.
func (m *Manager) waitRunning(ctx context.Context, podName string) (string, error) {
	var uid string
	err := wait.PollUntilContextTimeout(ctx, pollInterval, m.cfg.Timeout, true,
		func(ctx context.Context) (bool, error) {
			pod, err := m.cs.CoreV1().Pods(m.cfg.StagingNS).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return false, fmt.Errorf("staging: get pod: %w", err)
			}
			switch pod.Status.Phase {
			case corev1.PodRunning:
				uid = string(pod.UID)
				return true, nil
			case corev1.PodFailed, corev1.PodSucceeded:
				detail := ""
				for _, cs := range pod.Status.ContainerStatuses {
					if t := cs.State.Terminated; t != nil {
						detail = fmt.Sprintf(" (%s: %s)", t.Reason, t.Message)
						break
					}
				}
				return false, fmt.Errorf("staging: pod %q reached terminal phase %s unexpectedly%s", podName, pod.Status.Phase, detail)
			}
			return false, nil
		})
	return uid, err
}

// waitDeleted polls until the named pod no longer exists.
func (m *Manager) waitDeleted(ctx context.Context, podName string) error {
	return wait.PollUntilContextTimeout(ctx, pollInterval, m.cfg.Timeout, true,
		func(ctx context.Context) (bool, error) {
			_, err := m.cs.CoreV1().Pods(m.cfg.StagingNS).Get(ctx, podName, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, fmt.Errorf("staging: get pod during delete-wait: %w", err)
			}
			return false, nil // still terminating
		})
}

// buildPod constructs the staging pod spec.
func buildPod(podName, stagingNS, backingPVC, nodeName, pauseImage, volumeID string, taints []corev1.Taint) *corev1.Pod {
	tolerations := make([]corev1.Toleration, 0, len(taints))
	for _, t := range taints {
		tolerations = append(tolerations, corev1.Toleration{
			Key:      t.Key,
			Operator: corev1.TolerationOpEqual,
			Value:    t.Value,
			Effect:   t.Effect,
		})
	}
	falseVal := false
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: stagingNS,
			Labels: map[string]string{
				managedByLabel: managedByValue,
				componentLabel: "staging",
			},
			Annotations: map[string]string{
				volumeHashAnnot: sha256hex16(volumeID),
			},
		},
		Spec: corev1.PodSpec{
			NodeName:                     nodeName,
			Tolerations:                  tolerations,
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: &falseVal,
			Containers: []corev1.Container{{
				Name:  "pause",
				Image: pauseImage,
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &falseVal,
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
				},
				VolumeDevices: []corev1.VolumeDevice{{
					Name:       "backing",
					DevicePath: "/dev/backing",
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "backing",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: backingPVC,
					},
				},
			}},
		},
	}
}

// stagingPodName derives a deterministic pod name from the volume ID.
func stagingPodName(volumeID string) string {
	return "luks-stage-" + sha256hex16(volumeID)
}

// sha256hex16 returns the first 32 hex characters of the SHA-256 hash of s.
func sha256hex16(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}
