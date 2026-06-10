package staging_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/nfcs/k8s-luks-csi-go/internal/staging"
)

const (
	testStagingNS = "luks-csi-staging"
	testBackingNS = "luks-csi-backing"
	testNode      = "node-1"
	testPauseImg  = "registry.k8s.io/pause:3.9"
)

func newManager(cs *fake.Clientset) *staging.Manager {
	return staging.New(cs, staging.Config{
		NodeName:   testNode,
		StagingNS:  testStagingNS,
		BackingNS:  testBackingNS,
		PauseImage: testPauseImg,
	})
}

func fakePVC(cs *fake.Clientset, name string) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testBackingNS,
			UID:       types.UID("uid-" + name),
		},
	}
	_ = cs.Tracker().Add(pvc)
	return pvc
}

func fakeNode(cs *fake.Clientset, taints []corev1.Taint) {
	_ = cs.Tracker().Add(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: testNode},
		Spec:       corev1.NodeSpec{Taints: taints},
	})
}

// runningOnCreate installs a reactor that marks pods Running as soon as they
// are created. This avoids the polling timeout in waitRunning.
func runningOnCreate(cs *fake.Clientset) {
	cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		pod := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Status.Phase = corev1.PodRunning
		return false, pod, nil
	})
}

func sha256hex16(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

func TestEnsureStagedCreatesPodAndWaitsRunning(t *testing.T) {
	cs := fake.NewSimpleClientset()
	fakeNode(cs, nil)
	pvc := fakePVC(cs, "luks-backing-abc123")
	runningOnCreate(cs)

	if err := newManager(cs).EnsureStaged(context.Background(), "bristol/vol-1/Retain", map[string]string{
		"backingPVCName": pvc.Name,
	}); err != nil {
		t.Fatalf("EnsureStaged: %v", err)
	}

	pods, _ := cs.CoreV1().Pods(testStagingNS).List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(pods.Items))
	}
	pod := pods.Items[0]
	if pod.Spec.NodeName != testNode {
		t.Fatalf("spec.nodeName = %q, want %q", pod.Spec.NodeName, testNode)
	}
	if len(pod.Spec.Volumes) == 0 {
		t.Fatal("expected pod to mount backing PVC")
	}
	if pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != pvc.Name {
		t.Fatalf("pvc claimName = %q, want %q", pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName, pvc.Name)
	}
}

func TestEnsureStagedPropagatesNodeTaints(t *testing.T) {
	cs := fake.NewSimpleClientset()
	fakeNode(cs, []corev1.Taint{
		{Key: "node.kubernetes.io/unschedulable", Effect: corev1.TaintEffectNoExecute},
	})
	pvc := fakePVC(cs, "luks-backing-abc123")
	runningOnCreate(cs)

	_ = newManager(cs).EnsureStaged(context.Background(), "bristol/vol-1/Retain", map[string]string{
		"backingPVCName": pvc.Name,
	})

	pods, _ := cs.CoreV1().Pods(testStagingNS).List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) == 0 {
		t.Fatal("no pod created")
	}
	tols := pods.Items[0].Spec.Tolerations
	if len(tols) == 0 {
		t.Fatal("expected tolerations from node taints")
	}
	if tols[0].Key != "node.kubernetes.io/unschedulable" {
		t.Fatalf("toleration key = %q, want node.kubernetes.io/unschedulable", tols[0].Key)
	}
}

func TestEnsureStagedAnnotatesVolumeHash(t *testing.T) {
	cs := fake.NewSimpleClientset()
	fakeNode(cs, nil)
	pvc := fakePVC(cs, "luks-backing-abc123")
	runningOnCreate(cs)

	_ = newManager(cs).EnsureStaged(context.Background(), "bristol/vol-1/Retain", map[string]string{
		"backingPVCName": pvc.Name,
	})

	pods, _ := cs.CoreV1().Pods(testStagingNS).List(context.Background(), metav1.ListOptions{})
	ann := pods.Items[0].Annotations["luks.csi.nfcs.io/volume-hash"]
	if ann == "" {
		t.Fatal("volume-hash annotation missing")
	}
	if ann == "bristol/vol-1/Retain" {
		t.Fatal("volume-hash must be hashed, not raw volume ID")
	}
	if ann != sha256hex16("bristol/vol-1/Retain") {
		t.Fatalf("volume-hash = %q, want sha256hex16 of volumeID", ann)
	}
}

func TestEnsureStagedSetsOwnerReference(t *testing.T) {
	cs := fake.NewSimpleClientset()
	fakeNode(cs, nil)
	pvc := fakePVC(cs, "luks-backing-abc123")
	runningOnCreate(cs)

	_ = newManager(cs).EnsureStaged(context.Background(), "bristol/vol-1/Retain", map[string]string{
		"backingPVCName": pvc.Name,
	})

	pods, _ := cs.CoreV1().Pods(testStagingNS).List(context.Background(), metav1.ListOptions{})
	refs := pods.Items[0].OwnerReferences
	if len(refs) == 0 {
		t.Fatal("ownerReference missing")
	}
	if refs[0].Kind != "PersistentVolumeClaim" || refs[0].UID != pvc.UID {
		t.Fatalf("ownerRef = {kind:%s uid:%s}, want {PersistentVolumeClaim %s}", refs[0].Kind, refs[0].UID, pvc.UID)
	}
}

func TestEnsureStagedIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	fakeNode(cs, nil)
	pvc := fakePVC(cs, "luks-backing-abc123")
	runningOnCreate(cs)

	vc := map[string]string{"backingPVCName": pvc.Name}
	m := newManager(cs)
	if err := m.EnsureStaged(context.Background(), "bristol/vol-1/Retain", vc); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := m.EnsureStaged(context.Background(), "bristol/vol-1/Retain", vc); err != nil {
		t.Fatalf("second (idempotent): %v", err)
	}

	pods, _ := cs.CoreV1().Pods(testStagingNS).List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 1 {
		t.Fatalf("expected 1 pod after double EnsureStaged, got %d", len(pods.Items))
	}
}

func TestEnsureStagedDeletesStaleFailedPod(t *testing.T) {
	cs := fake.NewSimpleClientset()
	fakeNode(cs, nil)
	pvc := fakePVC(cs, "luks-backing-abc123")

	podName := "luks-stage-" + sha256hex16("bristol/vol-1/Retain")
	_ = cs.Tracker().Add(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: testStagingNS},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	})
	runningOnCreate(cs)

	if err := newManager(cs).EnsureStaged(context.Background(), "bristol/vol-1/Retain", map[string]string{
		"backingPVCName": pvc.Name,
	}); err != nil {
		t.Fatalf("EnsureStaged after stale pod: %v", err)
	}
}

func TestReleaseStagedDeletesPod(t *testing.T) {
	cs := fake.NewSimpleClientset()
	podName := "luks-stage-" + sha256hex16("bristol/vol-1/Retain")
	_ = cs.Tracker().Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: testStagingNS}})

	if err := newManager(cs).ReleaseStaged(context.Background(), "bristol/vol-1/Retain"); err != nil {
		t.Fatalf("ReleaseStaged: %v", err)
	}

	pods, _ := cs.CoreV1().Pods(testStagingNS).List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Fatalf("expected 0 pods after release, got %d", len(pods.Items))
	}
}

func TestReleaseStagedIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	if err := newManager(cs).ReleaseStaged(context.Background(), "bristol/vol-1/Retain"); err != nil {
		t.Fatalf("ReleaseStaged on absent pod: %v", err)
	}
}

func TestReconcileOrphansDeletesTerminalPods(t *testing.T) {
	cs := fake.NewSimpleClientset()
	labels := map[string]string{"app.kubernetes.io/managed-by": "luks-csi-driver"}
	for _, phase := range []corev1.PodPhase{corev1.PodFailed, corev1.PodSucceeded} {
		_ = cs.Tracker().Add(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-" + string(phase), Namespace: testStagingNS, Labels: labels},
			Status:     corev1.PodStatus{Phase: phase},
		})
	}
	_ = cs.Tracker().Add(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-running", Namespace: testStagingNS, Labels: labels},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	})

	if err := newManager(cs).ReconcileOrphans(context.Background()); err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}

	pods, _ := cs.CoreV1().Pods(testStagingNS).List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 1 || pods.Items[0].Name != "pod-running" {
		t.Fatalf("expected only pod-running to survive, got %v", pods.Items)
	}
}
