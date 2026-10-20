package device

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

// kubeletPublishSubdir is where kubelet publishes raw-block CSI volumes, one
// block device node per consuming pod: <subdir>/<pvName>/<podUID>. This is
// kubelet's on-disk layout rather than a stable API, so the result is
// cross-checked against /dev before it is returned.
const kubeletPublishSubdir = "plugins/kubernetes.io/csi/volumeDevices/publish"

// podUIDRE matches a Kubernetes pod UID (RFC 4122 UUID, lower-case).
var podUIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// devNameRE matches a sysfs DEVNAME: a node directly under /dev, optionally
// one directory deep (e.g. "rbd0", "sdd", "mapper/x").
var devNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}(/[a-zA-Z0-9_-]{1,64})?$`)

// rdevFunc stats path (following symlinks) and returns the major:minor of the
// block device it refers to, or an error if it is not a block device.
type rdevFunc func(path string) (major, minor uint32, err error)

// kubeletPublishDevice resolves the backing device for any CSI driver that
// supports volumeMode: Block, without backend-specific knowledge. Once the
// staging pod is Running, kubelet has bind-mounted the backing device node at
// <kubeletRoot>/<kubeletPublishSubdir>/<backingPV>/<stagingPodUID>; its
// major:minor is mapped to a /dev name through /sys/dev/block/<maj>:<min>/uevent.
type kubeletPublishDevice struct {
	kubeletRoot string
	sysRoot     string
	devRoot     string
	rdev        rdevFunc
}

func (k *kubeletPublishDevice) Name() string { return "kubelet" }

func (k *kubeletPublishDevice) Resolve(_ context.Context, params map[string]string) (string, error) {
	pvName := params["backingPV"]
	if err := validatePVName(pvName); err != nil {
		return "", err
	}
	podUID := params["stagingPodUID"]
	if !podUIDRE.MatchString(podUID) {
		return "", fmt.Errorf("device: kubelet: invalid stagingPodUID %q", podUID)
	}

	publish := filepath.Join(k.kubeletRoot, kubeletPublishSubdir, pvName, podUID)
	major, minor, err := k.rdev(publish)
	if err != nil {
		return "", fmt.Errorf("device: kubelet: published device: %w", err)
	}

	devName, err := k.devName(major, minor)
	if err != nil {
		return "", err
	}
	dev := filepath.Join(k.devRoot, devName)

	gotMajor, gotMinor, err := k.rdev(dev)
	if err != nil {
		return "", fmt.Errorf("device: kubelet: %w", err)
	}
	if gotMajor != major || gotMinor != minor {
		return "", fmt.Errorf("device: kubelet: rdev mismatch: %s is %d:%d, published device is %d:%d",
			dev, gotMajor, gotMinor, major, minor)
	}
	return dev, nil
}

// devName reads DEVNAME from the sysfs uevent for major:minor.
func (k *kubeletPublishDevice) devName(major, minor uint32) (string, error) {
	uevent := filepath.Join(k.sysRoot, "dev", "block", fmt.Sprintf("%d:%d", major, minor), "uevent")
	data, err := os.ReadFile(uevent)
	if err != nil {
		return "", fmt.Errorf("device: kubelet: read uevent: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if name, ok := strings.CutPrefix(sc.Text(), "DEVNAME="); ok {
			if !devNameRE.MatchString(name) {
				return "", fmt.Errorf("device: kubelet: unsafe DEVNAME %q for %d:%d", name, major, minor)
			}
			return name, nil
		}
	}
	return "", fmt.Errorf("device: kubelet: no DEVNAME in %s", uevent)
}

// statBlockRdev is the production rdevFunc.
func statBlockRdev(path string) (uint32, uint32, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if uint32(st.Mode)&unix.S_IFMT != unix.S_IFBLK {
		return 0, 0, fmt.Errorf("%s is not a block device", path)
	}
	rdev := uint64(st.Rdev) //nolint:unconvert // Rdev is int32 on darwin, uint64 on linux
	return unix.Major(rdev), unix.Minor(rdev), nil
}

// NewKubeletResolver returns a backend-agnostic Resolver that reads the device
// kubelet published for the staging pod. Requires stagingPodUID in params.
func NewKubeletResolver() Resolver {
	return &kubeletPublishDevice{
		kubeletRoot: "/var/lib/kubelet",
		sysRoot:     "/sys",
		devRoot:     "/dev",
		rdev:        statBlockRdev,
	}
}
