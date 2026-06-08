package device

import (
	"context"
	"fmt"
	"regexp"
)

// devicePathRE restricts raw device paths passed via volume context to
// block devices under /dev/. Prevents path traversal before shell exec.
var devicePathRE = regexp.MustCompile(`^/dev/[a-zA-Z0-9_./-]{1,128}$`)

// directDevice resolves a block device from the "devicePath" key in the
// volume context. Used for loop-device testing and static host-device setups;
// production workloads use by-id or attach-based resolvers.
type directDevice struct{}

func (directDevice) Name() string { return "direct" }

func (directDevice) Resolve(_ context.Context, params map[string]string) (string, error) {
	path := params["devicePath"]
	if path == "" {
		return "", fmt.Errorf("device: direct resolver requires devicePath in volume context")
	}
	if !devicePathRE.MatchString(path) {
		return "", fmt.Errorf("device: invalid devicePath %q", path)
	}
	return path, nil
}
