package device

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// longhornDevice resolves a Longhorn-backed volume by returning the predictable
// /dev/longhorn/<pvName> path. The staging pod (created before Resolve is
// called) has already triggered the Longhorn CSI driver's NodeStageVolume,
// which starts the Longhorn engine and creates the device node.
type longhornDevice struct{}

func (longhornDevice) Name() string { return "longhorn" }

func (longhornDevice) Resolve(_ context.Context, params map[string]string) (string, error) {
	pvName := params["backingPV"]
	if err := validatePVName(pvName); err != nil {
		return "", err
	}
	path := filepath.Clean("/dev/longhorn/" + pvName)
	if !strings.HasPrefix(path, "/dev/longhorn/") {
		return "", fmt.Errorf("device: backingPV %q escapes /dev/longhorn/", pvName)
	}
	return path, nil
}

// NewLonghornResolver returns a Resolver for Longhorn-backed volumes.
func NewLonghornResolver() Resolver {
	return longhornDevice{}
}
