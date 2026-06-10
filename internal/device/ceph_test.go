package device

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeCephStager records calls and returns a configurable error.
type fakeCephStager struct {
	gotVolumeID string
	err         error
}

func (f *fakeCephStager) Stage(_ context.Context, _, _, volumeID string,
	_, _ map[string]string) error {
	f.gotVolumeID = volumeID
	return f.err
}

// addCephPVAndSecret populates cs with a minimal Ceph RBD PV and its node-stage secret.
func addCephPVAndSecret(cs *fake.Clientset, pvName, pool, image string) {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: pvName},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "rbd.csi.ceph.com",
					VolumeHandle: "0001-0009-rook-ceph-0000000000000001-" + pvName,
					VolumeAttributes: map[string]string{
						"pool":      pool,
						"imageName": image,
						"clusterID": "rook-ceph",
					},
					NodeStageSecretRef: &corev1.SecretReference{
						Name:      "rook-csi-rbd-node",
						Namespace: "rook-ceph",
					},
				},
			},
		},
	}
	_ = cs.Tracker().Add(pv)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "rook-csi-rbd-node", Namespace: "rook-ceph"},
		Data:       map[string][]byte{"userID": []byte("admin"), "userKey": []byte("AQtest==")},
	}
	_ = cs.Tracker().Add(secret)
}

func fakeFinder(pool, image string) (string, error) {
	return "/dev/rbd0", nil
}

func newCephResolver(cs *fake.Clientset, stager cephNodeStager, stagingBase string) *cephRBDDevice {
	return &cephRBDDevice{
		blockAttachBase: newBlockAttachBase(cs, "node-1"),
		cs:              cs,
		socketPath:      "/fake/rbd.csi.ceph.com/csi.sock",
		stagingBase:     stagingBase,
		stager:          stager,
		finder:          fakeFinder,
	}
}

func TestCephResolverHappyPath(t *testing.T) {
	pvName := "pvc-7addc49f-e822-4465-a416-284d924dab4d"
	cs := fakeAttachedCS()
	addCephPVAndSecret(cs, pvName, "replicapool", "csi-vol-abc")
	stager := &fakeCephStager{}
	r := newCephResolver(cs, stager, t.TempDir())

	path, err := r.Resolve(context.Background(), map[string]string{"backingPV": pvName})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/dev/rbd0" {
		t.Fatalf("path = %q, want /dev/rbd0", path)
	}
	if stager.gotVolumeID == "" {
		t.Fatal("stager.Stage was not called")
	}
}

func TestCephResolverUsesCephAttacher(t *testing.T) {
	pvName := "pvc-abc"
	var gotAttacher string
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "volumeattachments",
		func(a k8stesting.Action) (bool, runtime.Object, error) {
			va := a.(k8stesting.CreateAction).GetObject().(*storagev1.VolumeAttachment)
			gotAttacher = va.Spec.Attacher
			va.Status.Attached = true
			return false, va, nil
		})
	addCephPVAndSecret(cs, pvName, "pool", "img")

	r := newCephResolver(cs, &fakeCephStager{}, t.TempDir())
	if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": pvName}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotAttacher != "rbd.csi.ceph.com" {
		t.Fatalf("attacher = %q, want rbd.csi.ceph.com", gotAttacher)
	}
}

func TestCephResolverMissingPoolOrImage(t *testing.T) {
	cases := []struct {
		name  string
		attrs map[string]string
	}{
		{"no pool", map[string]string{"imageName": "img"}},
		{"no imageName", map[string]string{"pool": "pool"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pvName := "pvc-abc"
			cs := fakeAttachedCS()
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: pvName},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:           "rbd.csi.ceph.com",
							VolumeHandle:     "handle",
							VolumeAttributes: tc.attrs,
							NodeStageSecretRef: &corev1.SecretReference{
								Name: "s", Namespace: "ns",
							},
						},
					},
				},
			}
			_ = cs.Tracker().Add(pv)
			_ = cs.Tracker().Add(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Data:       map[string][]byte{"k": []byte("v")},
			})
			r := newCephResolver(cs, &fakeCephStager{}, t.TempDir())
			if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": pvName}); err == nil {
				t.Fatal("expected error for missing pool/imageName")
			}
		})
	}
}

func TestCephResolverStagerError(t *testing.T) {
	pvName := "pvc-abc"
	cs := fakeAttachedCS()
	addCephPVAndSecret(cs, pvName, "pool", "img")
	stager := &fakeCephStager{err: fmt.Errorf("rbd map failed")}
	r := newCephResolver(cs, stager, t.TempDir())
	if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": pvName}); err == nil {
		t.Fatal("expected error when stager fails")
	}
}

func TestCephResolverRejectsInvalidBackingPV(t *testing.T) {
	r := NewCephResolver(fake.NewSimpleClientset(), "node-1")
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
