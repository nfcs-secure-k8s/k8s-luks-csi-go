package device

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	rbdSysfsBase = "/sys/bus/rbd/devices"
)

// rbdDeviceFinder locates the host block device for a given pool/image pair
// by scanning the kernel's rbd sysfs entries. Injected so tests can stub it.
type rbdDeviceFinder func(pool, image string) (string, error)

// sysfsRBDFinder scans /sys/bus/rbd/devices/{N}/{pool,name} to find
// /dev/rbd{N} for the given pool and image name.
func sysfsRBDFinder(pool, image string) (string, error) {
	entries, err := os.ReadDir(rbdSysfsBase)
	if err != nil {
		return "", fmt.Errorf("read rbd sysfs: %w", err)
	}
	for _, e := range entries {
		base := filepath.Join(rbdSysfsBase, e.Name())
		p, _ := os.ReadFile(filepath.Join(base, "pool"))
		n, _ := os.ReadFile(filepath.Join(base, "name"))
		if strings.TrimSpace(string(p)) == pool && strings.TrimSpace(string(n)) == image {
			return "/dev/rbd" + e.Name(), nil
		}
	}
	return "", fmt.Errorf("rbd device not found for %s/%s", pool, image)
}

// cephRBDDevice resolves a Ceph RBD-backed volume by:
//  1. Reading the PV to obtain pool and image name from CSI volumeAttributes.
//  2. Scanning /sys/bus/rbd/devices to find /dev/rbd{N}.
//
// The staging pod (created before Resolve is called) has already triggered
// rbd.csi.ceph.com's NodeStageVolume via kubelet, which runs rbd map and
// creates the /dev/rbd{N} device node on the host.
type cephRBDDevice struct {
	cs     kubernetes.Interface
	finder rbdDeviceFinder
}

func (c *cephRBDDevice) Name() string { return "ceph" }

func (c *cephRBDDevice) Resolve(ctx context.Context, params map[string]string) (string, error) {
	pvName := params["backingPV"]
	if err := validatePVName(pvName); err != nil {
		return "", err
	}

	pv, err := c.cs.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("device: ceph: get pv: %w", err)
	}
	if pv.Spec.CSI == nil {
		return "", fmt.Errorf("device: ceph: pv %q has no csi spec", pvName)
	}
	attrs := pv.Spec.CSI.VolumeAttributes
	pool, image := attrs["pool"], attrs["imageName"]
	if pool == "" || image == "" {
		return "", fmt.Errorf("device: ceph: pv %q missing pool or imageName in volumeAttributes", pvName)
	}

	dev, err := c.finder(pool, image)
	if err != nil {
		return "", fmt.Errorf("device: ceph: find rbd device: %w", err)
	}
	return dev, nil
}

// NewCephResolver returns a Resolver for Ceph RBD-backed volumes.
func NewCephResolver(cs kubernetes.Interface) Resolver {
	return &cephRBDDevice{cs: cs, finder: sysfsRBDFinder}
}
