package device

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testPV     = "pvc-f23939df-b6bf-4821-aeac-593cb7128613"
	testPodUID = "99e8e8f9-60ac-4876-a874-aba77ae63dfd"
)

// fakeRdev maps paths to major:minor; any other path is "not a block device".
type fakeRdev map[string][2]uint32

func (f fakeRdev) stat(path string) (uint32, uint32, error) {
	mm, ok := f[path]
	if !ok {
		return 0, 0, fmt.Errorf("%s: not a block device", path)
	}
	return mm[0], mm[1], nil
}

// newKubeletTestResolver builds a resolver over temp roots, with a sysfs
// uevent for 251:0 reporting devName, and the publish file + /dev node
// registered in rdev as 251:0.
func newKubeletTestResolver(t *testing.T, devName string) (*kubeletPublishDevice, fakeRdev) {
	t.Helper()
	root := t.TempDir()
	r := &kubeletPublishDevice{
		kubeletRoot: filepath.Join(root, "kubelet"),
		sysRoot:     filepath.Join(root, "sys"),
		devRoot:     filepath.Join(root, "dev"),
	}
	ueventDir := filepath.Join(r.sysRoot, "dev", "block", "251:0")
	if err := os.MkdirAll(ueventDir, 0o755); err != nil {
		t.Fatal(err)
	}
	uevent := "MAJOR=251\nMINOR=0\nDEVNAME=" + devName + "\nDEVTYPE=disk\n"
	if err := os.WriteFile(filepath.Join(ueventDir, "uevent"), []byte(uevent), 0o644); err != nil {
		t.Fatal(err)
	}
	rdev := fakeRdev{
		filepath.Join(r.kubeletRoot, "plugins/kubernetes.io/csi/volumeDevices/publish", testPV, testPodUID): {251, 0},
		filepath.Join(r.devRoot, devName): {251, 0},
	}
	r.rdev = rdev.stat
	return r, rdev
}

func kubeletParams() map[string]string {
	return map[string]string{"backingPV": testPV, "stagingPodUID": testPodUID}
}

func TestKubeletResolverResolvesPublishedDeviceToDevNode(t *testing.T) {
	r, _ := newKubeletTestResolver(t, "rbd0")
	path, err := r.Resolve(context.Background(), kubeletParams())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := filepath.Join(r.devRoot, "rbd0"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestKubeletResolverName(t *testing.T) {
	if got := NewKubeletResolver().Name(); got != "kubelet" {
		t.Fatalf("Name() = %q, want kubelet", got)
	}
}

func TestKubeletResolverRejectsInvalidPodUID(t *testing.T) {
	r, _ := newKubeletTestResolver(t, "rbd0")
	for _, uid := range []string{"", "../../etc", "not-a-uid", testPodUID + "/x"} {
		p := kubeletParams()
		p["stagingPodUID"] = uid
		if _, err := r.Resolve(context.Background(), p); err == nil {
			t.Fatalf("expected rejection for stagingPodUID %q", uid)
		}
	}
}

func TestKubeletResolverRejectsInvalidBackingPV(t *testing.T) {
	r, _ := newKubeletTestResolver(t, "rbd0")
	p := kubeletParams()
	p["backingPV"] = "../../etc/passwd"
	if _, err := r.Resolve(context.Background(), p); err == nil {
		t.Fatal("expected rejection for traversal backingPV")
	}
}

func TestKubeletResolverFailsWhenPublishPathIsNotBlockDevice(t *testing.T) {
	r, rdev := newKubeletTestResolver(t, "rbd0")
	delete(rdev, filepath.Join(r.kubeletRoot, "plugins/kubernetes.io/csi/volumeDevices/publish", testPV, testPodUID))
	if _, err := r.Resolve(context.Background(), kubeletParams()); err == nil {
		t.Fatal("expected error when publish path is not a block device")
	}
}

func TestKubeletResolverRejectsUnsafeDevName(t *testing.T) {
	for _, name := range []string{"../etc/passwd", "a/../../b", ""} {
		r, _ := newKubeletTestResolver(t, name)
		if _, err := r.Resolve(context.Background(), kubeletParams()); err == nil {
			t.Fatalf("expected rejection for DEVNAME %q", name)
		}
	}
}

func TestKubeletResolverFailsWhenDevNodeRdevMismatches(t *testing.T) {
	r, rdev := newKubeletTestResolver(t, "rbd0")
	rdev[filepath.Join(r.devRoot, "rbd0")] = [2]uint32{8, 48}
	_, err := r.Resolve(context.Background(), kubeletParams())
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("err = %v, want rdev mismatch error", err)
	}
}
