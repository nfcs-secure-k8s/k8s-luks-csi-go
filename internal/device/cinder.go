package device

import (
	"context"
)

// cinderDevice resolves an OpenStack Cinder-backed volume from the "devicePath"
// key in the volume context. The staging pod (created before Resolve is called)
// triggers Cinder's full ControllerPublishVolume -> NodeStageVolume flow via
// kubelet, making the device appear at the configured path.
//
// Operators must set devicePath in the StorageClass parameters for Cinder
// volumes. Auto-discovery via /dev/disk/by-id is left for a follow-up.
type cinderDevice struct{}

func (cinderDevice) Name() string { return "cinder" }

func (cinderDevice) Resolve(ctx context.Context, params map[string]string) (string, error) {
	// Delegate to the directDevice resolver — it validates the path format.
	return directDevice{}.Resolve(ctx, params)
}

// NewCinderResolver returns a Resolver for OpenStack Cinder-backed volumes.
func NewCinderResolver() Resolver {
	return cinderDevice{}
}
