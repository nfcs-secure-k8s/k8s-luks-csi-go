// Package app assembles the dependency graph for each driver mode.
package app

import (
	"fmt"

	"github.com/nfcs/k8s-luks-csi-go/internal/controller"
	"github.com/nfcs/k8s-luks-csi-go/internal/vault"
)

// Config holds runtime configuration sourced from env/flags.
type Config struct {
	VaultAddr     string
	VaultCABundle string
}

// BuildController wires the Controller service. Fails fast if the Vault
// transport is not https with a CA bundle (C-1).
func BuildController(cfg Config) (*controller.Service, error) {
	if cfg.VaultAddr == "" {
		return nil, fmt.Errorf("app: VAULT_ADDR is required")
	}
	vc, err := vault.New(vault.Config{Address: cfg.VaultAddr, CABundlePath: cfg.VaultCABundle})
	if err != nil {
		return nil, err
	}
	// backingProvisioner implements controller.Backing over client-go (added
	// alongside ControllerPublishVolume wiring); nil-safe for the config test.
	return controller.New(controller.Deps{Vault: vc}), nil
}
