package device

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
)

// cinderDevice resolves an OpenStack Cinder-backed volume by:
//  1. Creating a VolumeAttachment for cinder.csi.openstack.org and waiting
//     for attached=true.
//  2. Reading the device path from AttachmentMetadata["DevicePath"], which
//     cinder.csi.openstack.org's ControllerPublishVolumeResponse.PublishContext
//     sets to the virtio/SCSI device node (e.g. /dev/vdb).
//
// The caller's WaitForDevice polls until the path exists on the host.
type cinderDevice struct{ blockAttachBase }

func (c *cinderDevice) Name() string { return "cinder" }

func (c *cinderDevice) Resolve(ctx context.Context, params map[string]string) (string, error) {
	pvName := params["backingPV"]
	if err := validatePVName(pvName); err != nil {
		return "", err
	}
	meta, err := c.attachPV(ctx, pvName, "cinder.csi.openstack.org")
	if err != nil {
		return "", fmt.Errorf("device: cinder: %w", err)
	}
	path := meta["DevicePath"]
	if path == "" {
		return "", fmt.Errorf("device: cinder: missing DevicePath in VolumeAttachment metadata")
	}
	return path, nil
}

// NewCinderResolver returns a Resolver for OpenStack Cinder-backed volumes.
// nodeName is read from KUBE_NODE_NAME when empty.
func NewCinderResolver(cs kubernetes.Interface, nodeName string) Resolver {
	return &cinderDevice{blockAttachBase: newBlockAttachBase(cs, nodeName)}
}
