package app

import (
	"context"

	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
	"github.com/nfcs/k8s-luks-csi-go/internal/node"
	"github.com/nfcs/k8s-luks-csi-go/internal/vault"
)

// nodeKeySource fetches volume keys by logging in with the per-tenant AppRole
// from the node-stage secret, then reading the key from Vault KV v2 (H-1).
type nodeKeySource struct {
	vault *vault.Client
}

func (n *nodeKeySource) KeyFor(ctx context.Context, secret node.Secret, institution, volume string) (*keyzero.Buffer, error) {
	cancel, err := n.vault.LoginAppRole(ctx, secret.RoleID, secret.SecretID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return n.vault.ReadKey(ctx, institution, volume)
}
