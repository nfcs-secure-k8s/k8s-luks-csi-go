package luks

import (
	"context"
	"fmt"

	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
	"github.com/nfcs/k8s-luks-csi-go/internal/validate"
)

// LUKS wraps a Runner with idempotent cryptsetup/mkfs operations.
type LUKS struct {
	run  Runner
	mkfs Runner
}

// New returns a LUKS using the given cryptsetup Runner. MakeFS defaults to mkfs.ext4.
func New(cryptsetup Runner) *LUKS {
	return &LUKS{run: cryptsetup, mkfs: NewExecRunner("mkfs.ext4")}
}

// WithMkfs overrides the filesystem-creation Runner (for tests/other fs).
func (l *LUKS) WithMkfs(r Runner) *LUKS { l.mkfs = r; return l }

// Format initialises a LUKS2 container with argon2id. luks1 is never used (M-2).
func (l *LUKS) Format(ctx context.Context, device string, key *keyzero.Buffer) error {
	_, err := l.run.Run(ctx, key,
		"luksFormat", "--type", "luks2", "--pbkdf", "argon2id",
		"--batch-mode", "--key-file="+KeyFDPath, device)
	if err != nil {
		return fmt.Errorf("luks: format %s: %w", device, err)
	}
	return nil
}

// Open activates the mapper. Idempotent: if the mapper is already active it
// returns nil without re-running luksOpen.
func (l *LUKS) Open(ctx context.Context, device, name string, key *keyzero.Buffer) error {
	if err := validate.MapperName(name); err != nil {
		return err
	}
	if l.active(ctx, name) {
		return nil
	}
	_, err := l.run.Run(ctx, key, "luksOpen", "--key-file="+KeyFDPath, device, name)
	if err != nil {
		return fmt.Errorf("luks: open %s: %w", name, err)
	}
	return nil
}

// Close deactivates the mapper. Idempotent: inactive mapper returns nil.
func (l *LUKS) Close(ctx context.Context, name string) error {
	if err := validate.MapperName(name); err != nil {
		return err
	}
	if !l.active(ctx, name) {
		return nil
	}
	if _, err := l.run.Run(ctx, nil, "luksClose", name); err != nil {
		return fmt.Errorf("luks: close %s: %w", name, err)
	}
	return nil
}

// MakeFS creates a filesystem on the opened mapper device.
func (l *LUKS) MakeFS(ctx context.Context, name string) error {
	if err := validate.MapperName(name); err != nil {
		return err
	}
	if _, err := l.mkfs.Run(ctx, nil, "/dev/mapper/"+name); err != nil {
		return fmt.Errorf("luks: mkfs %s: %w", name, err)
	}
	return nil
}

// AddKey adds newKey as an additional keyslot, authenticating with oldKey.
func (l *LUKS) AddKey(ctx context.Context, device string, oldKey, newKey *keyzero.Buffer) error {
	_, err := l.run.Run(ctx, oldKey,
		"luksAddKey", "--key-file="+KeyFDPath, device, "/dev/fd/4")
	if err != nil {
		return fmt.Errorf("luks: addkey %s: %w", device, err)
	}
	return nil
}

// RemoveKey removes the keyslot matching the supplied key.
func (l *LUKS) RemoveKey(ctx context.Context, device string, key *keyzero.Buffer) error {
	if _, err := l.run.Run(ctx, key, "luksRemoveKey", "--key-file="+KeyFDPath, device); err != nil {
		return fmt.Errorf("luks: removekey %s: %w", device, err)
	}
	return nil
}

// IsLUKS reports whether device already holds a LUKS header.
func (l *LUKS) IsLUKS(ctx context.Context, device string) (bool, error) {
	_, err := l.run.Run(ctx, nil, "isLuks", device)
	return err == nil, nil
}

// active reports whether the named mapper is currently active.
func (l *LUKS) active(ctx context.Context, name string) bool {
	out, _ := l.run.Run(ctx, nil, "status", name)
	return len(out) > 0
}
