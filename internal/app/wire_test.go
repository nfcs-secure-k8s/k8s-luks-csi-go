package app

import "testing"

func TestBuildControllerRequiresVaultConfig(t *testing.T) {
	// Missing VAULT_ADDR must fail fast (C-1: no insecure default).
	_, err := BuildController(Config{VaultAddr: "", VaultCABundle: "/tmp/ca"})
	if err == nil {
		t.Fatal("BuildController accepted an empty Vault address")
	}
}

func TestBuildControllerRejectsHTTPVault(t *testing.T) {
	_, err := BuildController(Config{VaultAddr: "http://v:8200", VaultCABundle: "/tmp/ca"})
	if err == nil {
		t.Fatal("BuildController accepted a plaintext Vault address")
	}
}
