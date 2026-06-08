package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// execMounter implements node.Mounter using system mount/umount binaries.
// Requires root privileges (IPC_LOCK + mount in the node container spec).
type execMounter struct{}

func (m *execMounter) Mount(ctx context.Context, source, target string) error {
	if err := os.MkdirAll(target, 0o750); err != nil {
		return fmt.Errorf("mount: mkdir %s: %w", target, err)
	}
	cmd := exec.CommandContext(ctx, "mount", "-t", "ext4", source, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount: %s → %s: %w: %s", source, target, err, out)
	}
	return nil
}

func (m *execMounter) BindMount(ctx context.Context, source, target string) error {
	if err := os.MkdirAll(target, 0o750); err != nil {
		return fmt.Errorf("mount: mkdir %s: %w", target, err)
	}
	cmd := exec.CommandContext(ctx, "mount", "--bind", source, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount: bind %s → %s: %w: %s", source, target, err, out)
	}
	return nil
}

func (m *execMounter) Unmount(ctx context.Context, target string) error {
	cmd := exec.CommandContext(ctx, "umount", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount: umount %s: %w: %s", target, err, out)
	}
	return nil
}
