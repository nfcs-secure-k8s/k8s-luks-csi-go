package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
	"github.com/nfcs/k8s-luks-csi-go/internal/validate"
)

const mountPath = "secret"

var errNotFound = errors.New("vault: key not found")

// Config configures the Vault transport.
type Config struct {
	Address      string // must be https://
	CABundlePath string // PEM CA bundle; required
}

// kv abstracts KV v2 operations for testability.
type kv interface {
	Get(ctx context.Context, path string) (key []byte, version int, err error)
	GetVersion(ctx context.Context, path string, version int) ([]byte, error)
	Put(ctx context.Context, path string, key []byte) (version int, err error)
	DeleteAll(ctx context.Context, path string) error
}

// Client is a per-tenant-scoped Vault key client.
type Client struct {
	api    *vaultapi.Client
	kv     kv
	genKey func(n int) ([]byte, error) // nil => crypto/rand (overridable in tests)
}

// New builds a TLS-only Vault client. Refuses non-https or missing CA bundle (C-1).
func New(cfg Config) (*Client, error) {
	if !strings.HasPrefix(strings.ToLower(cfg.Address), "https://") {
		return nil, fmt.Errorf("vault: address must use https, got %q", cfg.Address)
	}
	if cfg.CABundlePath == "" {
		return nil, errors.New("vault: CA bundle path is required")
	}
	apiCfg := vaultapi.DefaultConfig()
	apiCfg.Address = cfg.Address
	if err := apiCfg.ConfigureTLS(&vaultapi.TLSConfig{CACert: cfg.CABundlePath}); err != nil {
		return nil, fmt.Errorf("vault: configure TLS: %w", err)
	}
	api, err := vaultapi.NewClient(apiCfg)
	if err != nil {
		return nil, fmt.Errorf("vault: new client: %w", err)
	}
	return &Client{api: api, kv: &apiKV{api: api}}, nil
}

// keyPath validates inputs and builds the per-tenant KV path (M-5/H-1).
func keyPath(institution, volume string) (string, error) {
	if err := validate.Institution(institution); err != nil {
		return "", err
	}
	if err := validate.VolumeName(volume); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/%s", institution, volume), nil
}

// ReadKey fetches the current key version into an mlock'd buffer.
func (c *Client) ReadKey(ctx context.Context, institution, volume string) (*keyzero.Buffer, error) {
	p, err := keyPath(institution, volume)
	if err != nil {
		return nil, err
	}
	raw, _, err := c.kv.Get(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("vault: read key: %w", err)
	}
	return toBuffer(raw)
}

// CurrentVersion returns the latest version number for a volume's key.
func (c *Client) CurrentVersion(ctx context.Context, institution, volume string) (int, error) {
	p, err := keyPath(institution, volume)
	if err != nil {
		return 0, err
	}
	_, v, err := c.kv.Get(ctx, p)
	return v, err
}

func toBuffer(raw []byte) (*keyzero.Buffer, error) {
	buf, err := keyzero.NewBuffer(len(raw))
	if err != nil {
		return nil, err
	}
	copy(buf.Bytes(), raw)
	return buf, nil
}
