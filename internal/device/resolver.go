package device

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"
)

// Resolver maps a backing volume_context to a host block device path.
type Resolver interface {
	Name() string
	Resolve(ctx context.Context, params map[string]string) (string, error)
}

// Registry dispatches resolvers by backend name.
type Registry struct{ resolvers map[string]Resolver }

func NewRegistry() *Registry { return &Registry{resolvers: map[string]Resolver{}} }

func (r *Registry) Register(res Resolver) { r.resolvers[res.Name()] = res }

func (r *Registry) Get(name string) (Resolver, error) {
	res, ok := r.resolvers[name]
	if !ok {
		return nil, fmt.Errorf("device: no resolver for backend %q", name)
	}
	return res, nil
}

// serialRE guards device identifiers used to build /dev/disk/by-id paths.
var serialRE = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)

// genericByID resolves cloud/attach-based block devices by stable serial.
type genericByID struct{}

func (genericByID) Name() string { return "by-id" }
func (genericByID) Resolve(_ context.Context, params map[string]string) (string, error) {
	serial := params["serial"]
	if !serialRE.MatchString(serial) {
		return "", fmt.Errorf("device: invalid serial %q", serial)
	}
	return "/dev/disk/by-id/" + serial, nil
}

// WaitForDevice blocks until path exists or the context/deadline elapses.
func WaitForDevice(ctx context.Context, path string, timeout time.Duration) error {
	return waitForDevice(ctx, path, 200*time.Millisecond, timeout, func(p string) error {
		_, err := os.Stat(p)
		return err
	})
}

func waitForDevice(ctx context.Context, path string, interval, timeout time.Duration, stat func(string) error) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	if stat(path) == nil {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("device: wait for %s: %w", path, ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("device: timed out waiting for %s", path)
		case <-tick.C:
			if stat(path) == nil {
				return nil
			}
		}
	}
}
