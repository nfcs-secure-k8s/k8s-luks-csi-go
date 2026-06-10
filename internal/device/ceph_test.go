package device

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// addCephPV populates cs with a minimal Ceph RBD PV.
// No secret is needed — kubelet passes node-stage secrets when it processes the
// staging pod's PVC mount, not this resolver.
func addCephPV(cs *fake.Clientset, pvName, pool, image string) {
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
					},
				},
			},
		},
	}
	_ = cs.Tracker().Add(pv)
}

func newCephTestResolver(cs *fake.Clientset, finder rbdDeviceFinder) *cephRBDDevice {
	return &cephRBDDevice{cs: cs, finder: finder}
}

func TestCephResolverHappyPath(t *testing.T) {
	pvName := "pvc-7addc49f-e822-4465-a416-284d924dab4d"
	cs := fake.NewSimpleClientset()
	addCephPV(cs, pvName, "replicapool", "csi-vol-abc")

	r := newCephTestResolver(cs, fakeFinder)
	path, err := r.Resolve(context.Background(), map[string]string{"backingPV": pvName})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/dev/rbd0" {
		t.Fatalf("path = %q, want /dev/rbd0", path)
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
			cs := fake.NewSimpleClientset()
			_ = cs.Tracker().Add(&corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: pvName},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:           "rbd.csi.ceph.com",
							VolumeHandle:     "handle",
							VolumeAttributes: tc.attrs,
						},
					},
				},
			})
			r := newCephTestResolver(cs, fakeFinder)
			if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": pvName}); err == nil {
				t.Fatal("expected error for missing pool/imageName")
			}
		})
	}
}

func TestCephResolverSysfsError(t *testing.T) {
	pvName := "pvc-abc"
	cs := fake.NewSimpleClientset()
	addCephPV(cs, pvName, "pool", "img")

	errFinder := func(_, _ string) (string, error) { return "", fmt.Errorf("no rbd device") }
	r := newCephTestResolver(cs, errFinder)
	if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": pvName}); err == nil {
		t.Fatal("expected error when sysfs scan fails")
	}
}

func TestCephResolverRejectsInvalidBackingPV(t *testing.T) {
	r := NewCephResolver(fake.NewSimpleClientset())
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
