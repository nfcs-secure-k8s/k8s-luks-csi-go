package device

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
)

// cephRBDDevice resolves a Ceph RBD-backed volume by:
//  1. Creating a VolumeAttachment for rbd.csi.ceph.com and waiting for
//     attached=true (the Ceph CSI driver's external-attacher runs luksOpen
//     equivalent and populates AttachmentMetadata).
//  2. Reading the device path from AttachmentMetadata["devicePath"], which
//     rbd.csi.ceph.com's ControllerPublishVolumeResponse.PublishContext sets
//     to the mapped RBD block device (e.g. /dev/rbd0).
//
// The caller's WaitForDevice polls until the path exists on the host.
type cephRBDDevice struct{ blockAttachBase }

func (c *cephRBDDevice) Name() string { return "ceph" }

func (c *cephRBDDevice) Resolve(ctx context.Context, params map[string]string) (string, error) {
	pvName := params["backingPV"]
	if err := validatePVName(pvName); err != nil {
		return "", err
	}
	meta, err := c.attachPV(ctx, pvName, "rbd.csi.ceph.com")
	if err != nil {
		return "", fmt.Errorf("device: ceph: %w", err)
	}
	path := meta["devicePath"]
	if path == "" {
		return "", fmt.Errorf("device: ceph: missing devicePath in VolumeAttachment metadata")
	}
	return path, nil
}

// NewCephResolver returns a Resolver for Ceph RBD-backed volumes.
// nodeName is read from KUBE_NODE_NAME when empty.
func NewCephResolver(cs kubernetes.Interface, nodeName string) Resolver {
	return &cephRBDDevice{blockAttachBase: newBlockAttachBase(cs, nodeName)}
}
