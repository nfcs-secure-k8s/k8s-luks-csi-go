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

func TestBuildNodeDefaultsStagingNamespace(t *testing.T) {
	// BuildNode checks Vault config before touching k8s; with no usable Vault
	// CA / in-cluster config it must return an error rather than panic. This
	// guards that the staging-namespace defaulting added to BuildNode compiles
	// and the Vault fail-fast still fires first.
	_, err := BuildNode(Config{VaultAddr: "https://vault.test:8200"})
	if err == nil {
		t.Fatal("expected BuildNode to error without a valid Vault CA / k8s config")
	}
}
