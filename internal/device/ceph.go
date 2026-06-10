package device

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	cephDefaultSocket      = "/var/lib/kubelet/plugins/rbd.csi.ceph.com/csi.sock"
	cephDefaultStagingBase = "/var/lib/kubelet/plugins/rbd.csi.ceph.com/luks-staging"
	rbdSysfsBase           = "/sys/bus/rbd/devices"
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

// cephNodeStager calls the Ceph CSI node plugin to map an RBD block device.
// Separated from cephRBDDevice so tests can inject a mock.
type cephNodeStager interface {
	Stage(ctx context.Context, socketPath, stagingPath, volumeID string,
		volumeContext, secrets map[string]string) error
}

// liveCephStager connects to the Ceph node plugin socket and calls NodeStageVolume.
type liveCephStager struct{}

func (liveCephStager) Stage(ctx context.Context, socketPath, stagingPath, volumeID string,
	volumeContext, secrets map[string]string) error {

	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("connect to ceph socket: %w", err)
	}
	defer conn.Close()

	_, err = csi.NewNodeClient(conn).NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{
		VolumeId:          volumeID,
		StagingTargetPath: stagingPath,
		VolumeContext:     volumeContext,
		Secrets:           secrets,
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
			AccessType: &csi.VolumeCapability_Block{
				Block: &csi.VolumeCapability_BlockVolume{},
			},
		},
	})
	return err
}

// cephRBDDevice resolves a Ceph RBD-backed volume by:
//  1. Creating a VolumeAttachment for rbd.csi.ceph.com and waiting for
//     attached=true (ControllerPublishVolume validates node access).
//  2. Reading the PV's CSI volumeAttributes (pool, imageName) and
//     nodeStageSecretRef (Ceph credentials).
//  3. Calling NodeStageVolume on the Ceph node plugin socket, which
//     runs rbd-map and makes the device available at /dev/rbd{N}.
//  4. Scanning /sys/bus/rbd/devices to find the actual /dev/rbd{N} path
//     (the udev symlink at /dev/rbd/{pool}/{image} is not always present).
type cephRBDDevice struct {
	blockAttachBase
	cs          kubernetes.Interface
	socketPath  string
	stagingBase string
	stager      cephNodeStager
	finder      rbdDeviceFinder
}

func (c *cephRBDDevice) Name() string { return "ceph" }

func (c *cephRBDDevice) Resolve(ctx context.Context, params map[string]string) (string, error) {
	pvName := params["backingPV"]
	if err := validatePVName(pvName); err != nil {
		return "", err
	}

	// Validate controller-side access via VolumeAttachment.
	if _, err := c.attachPV(ctx, pvName, "rbd.csi.ceph.com"); err != nil {
		return "", fmt.Errorf("device: ceph: attach: %w", err)
	}

	// Read the backing PV to obtain CSI volume attributes.
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

	// Read the node-stage secret to get Ceph credentials.
	ref := pv.Spec.CSI.NodeStageSecretRef
	secret, err := c.cs.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("device: ceph: get stage secret: %w", err)
	}
	creds := make(map[string]string, len(secret.Data))
	for k, v := range secret.Data {
		creds[k] = string(v)
	}

	// Call the Ceph node plugin NodeStageVolume which runs rbd map.
	stagingPath := filepath.Join(c.stagingBase, pvName)
	if err := os.MkdirAll(stagingPath, 0o700); err != nil {
		return "", fmt.Errorf("device: ceph: mkdir staging: %w", err)
	}
	if err := c.stager.Stage(ctx, c.socketPath, stagingPath,
		pv.Spec.CSI.VolumeHandle, attrs, creds); err != nil {
		return "", fmt.Errorf("device: ceph: node stage: %w", err)
	}

	// rbd map creates /dev/rbd{N}; scan sysfs to find the device number.
	// The udev symlink at /dev/rbd/{pool}/{image} is not reliably present.
	dev, err := c.finder(pool, image)
	if err != nil {
		return "", fmt.Errorf("device: ceph: find rbd device: %w", err)
	}
	return dev, nil
}

// NewCephResolver returns a Resolver for Ceph RBD-backed volumes.
// nodeName is read from KUBE_NODE_NAME when empty.
func NewCephResolver(cs kubernetes.Interface, nodeName string) Resolver {
	return &cephRBDDevice{
		blockAttachBase: newBlockAttachBase(cs, nodeName),
		cs:              cs,
		socketPath:      cephDefaultSocket,
		stagingBase:     cephDefaultStagingBase,
		stager:          liveCephStager{},
		finder:          sysfsRBDFinder,
	}
}
