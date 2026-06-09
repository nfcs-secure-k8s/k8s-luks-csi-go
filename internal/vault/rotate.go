package vault

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"

	"github.com/nfcs/k8s-luks-csi-go/internal/dbg"
	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
)

// FindAuthenticatingVersion walks Vault key versions newest→oldest, returning
// the first whose bytes satisfy tryOpen. Makes a half-applied rotation
// recoverable instead of bricking the volume (H-12).
func (c *Client) FindAuthenticatingVersion(
	ctx context.Context, institution, volume string, tryOpen func([]byte) bool,
) (*keyzero.Buffer, int, error) {
	p, err := keyPath(institution, volume)
	if err != nil {
		return nil, 0, err
	}
	_, current, err := c.kv.Get(ctx, p)
	if err != nil {
		return nil, 0, fmt.Errorf("vault: rotate: read current: %w", err)
	}
	for v := current; v >= 1; v-- {
		raw, err := c.kv.GetVersion(ctx, p, v)
		if err != nil {
			continue
		}
		if tryOpen(raw) {
			buf, err := toBuffer(raw)
			if err != nil {
				return nil, 0, err
			}
			return buf, v, nil
		}
		// Zero this candidate before trying the next — key material must not linger.
		for i := range raw {
			raw[i] = 0
		}
	}
	return nil, 0, fmt.Errorf("vault: no key version authenticates for %s/%s", institution, volume)
}

// EnsureKey creates a fresh random key for (institution, volume) only if none
// exists. Idempotent (H-12 rotation safety, M-6 Retain default).
func (c *Client) EnsureKey(ctx context.Context, institution, volume string) error {
	p, err := keyPath(institution, volume)
	if err != nil {
		return err
	}
	if dbg.Enabled {
		slog.Debug("vault: ensure key", "path", mountPath+"/data/"+p)
	}
	if _, _, err := c.kv.Get(ctx, p); err == nil {
		if dbg.Enabled {
			slog.Debug("vault: key already exists")
		}
		return nil
	}
	key, err := c.generate(32)
	if err != nil {
		slog.Warn("vault: ensure key failed", "op", "generate")
		return fmt.Errorf("vault: generate key: %w", err)
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	if _, err := c.kv.Put(ctx, p, key); err != nil {
		slog.Warn("vault: ensure key failed", "op", "put")
		return fmt.Errorf("vault: ensure key: %w", err)
	}
	if dbg.Enabled {
		slog.Debug("vault: key created")
	}
	return nil
}

// DeleteKey removes all versions of a volume's key. Only called when
// deletionPolicy=Delete (M-6 — default is Retain, a no-op).
func (c *Client) DeleteKey(ctx context.Context, institution, volume string) error {
	p, err := keyPath(institution, volume)
	if err != nil {
		return err
	}
	if dbg.Enabled {
		slog.Debug("vault: delete key", "path", mountPath+"/data/"+p)
	}
	if err := c.kv.DeleteAll(ctx, p); err != nil {
		slog.Warn("vault: delete key failed")
		return err
	}
	if dbg.Enabled {
		slog.Debug("vault: key deleted")
	}
	return nil
}

// generate produces n random bytes; overridable in tests via Client.genKey.
func (c *Client) generate(n int) ([]byte, error) {
	if c.genKey != nil {
		return c.genKey(n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
