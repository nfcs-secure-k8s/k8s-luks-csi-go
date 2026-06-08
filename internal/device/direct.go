package device

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// devicePathRE accepts simple block-device nodes: /dev/<name> or /dev/<dir>/<name>
// where each component is alphanumeric plus _ and -. Dots and extra slashes are
// rejected here to prevent traversal sequences like /dev/../etc/passwd.
var devicePathRE = regexp.MustCompile(`^/dev/[a-zA-Z0-9_-]{1,64}(/[a-zA-Z0-9_-]{1,64})?$`)

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
	// Reject traversal sequences before any other check.
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("device: invalid devicePath %q", path)
	}
	if !devicePathRE.MatchString(path) {
		return "", fmt.Errorf("device: invalid devicePath %q", path)
	}
	// Defense-in-depth: clean the path and re-verify it stays under /dev/.
	clean := filepath.Clean(path)
	if !strings.HasPrefix(clean, "/dev/") {
		return "", fmt.Errorf("device: devicePath %q escapes /dev/", path)
	}
	return clean, nil
}
