package app

import (
	"context"
	"time"

	"github.com/nfcs/k8s-luks-csi-go/internal/device"
)

// deviceAdapter bridges device.Registry to the node.DeviceOps interface.
// The "backend" key in volumeContext selects the resolver (default: "direct").
// Attach-based backends additionally wait for the device to appear.
type deviceAdapter struct {
	reg *device.Registry
}

func (d *deviceAdapter) ResolveAndAttach(ctx context.Context, volumeContext map[string]string) (string, error) {
	backend := volumeContext["backend"]
	if backend == "" {
		backend = "direct"
	}
	resolver, err := d.reg.Get(backend)
	if err != nil {
		return "", err
	}
	devicePath, err := resolver.Resolve(ctx, volumeContext)
	if err != nil {
		return "", err
	}
	// For attach-based backends the device may not be present immediately;
	// poll until it appears. Direct/local devices already exist.
	if backend != "direct" {
		if err := device.WaitForDevice(ctx, devicePath, 2*time.Minute); err != nil {
			return "", err
		}
	}
	return devicePath, nil
}
