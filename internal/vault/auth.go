package vault

import (
	"context"
	"fmt"

	vaultapi "github.com/hashicorp/vault/api"
	approle "github.com/hashicorp/vault/api/auth/approle"
)

// LoginAppRole authenticates with a per-tenant AppRole (role reference from
// the workload-namespace node-stage secret) and starts lease renewal.
// The returned cancel func stops the renewal goroutine (H-6).
func (c *Client) LoginAppRole(ctx context.Context, roleID, secretID string) (context.CancelFunc, error) {
	auth, err := approle.NewAppRoleAuth(roleID, &approle.SecretID{FromString: secretID})
	if err != nil {
		return nil, fmt.Errorf("vault: approle auth init: %w", err)
	}
	authInfo, err := c.api.Auth().Login(ctx, auth)
	if err != nil {
		return nil, fmt.Errorf("vault: approle login: %w", err)
	}
	if authInfo == nil {
		return nil, fmt.Errorf("vault: approle login returned no auth info")
	}
	renewCtx, cancel := context.WithCancel(ctx)
	go c.renew(renewCtx, authInfo)
	return cancel, nil
}

func (c *Client) renew(ctx context.Context, authInfo *vaultapi.Secret) {
	watcher, err := c.api.NewLifetimeWatcher(&vaultapi.LifetimeWatcherInput{Secret: authInfo})
	if err != nil {
		return
	}
	go watcher.Start()
	defer watcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watcher.DoneCh():
			return
		case <-watcher.RenewCh():
			// renewed; loop
		}
	}
}
