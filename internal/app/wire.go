// Package app assembles the dependency graph for each driver mode.
package app

import (
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/nfcs/k8s-luks-csi-go/internal/controller"
	"github.com/nfcs/k8s-luks-csi-go/internal/device"
	"github.com/nfcs/k8s-luks-csi-go/internal/luks"
	"github.com/nfcs/k8s-luks-csi-go/internal/node"
	"github.com/nfcs/k8s-luks-csi-go/internal/vault"
)

// Config holds runtime configuration sourced from env/flags.
type Config struct {
	VaultAddr        string
	VaultCABundle    string
	BackingNamespace string // namespace for backing PVCs (default: luks-csi-backing)
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
	ns := cfg.BackingNamespace
	if ns == "" {
		ns = "luks-csi-backing"
	}
	cs, err := newKubeClient()
	if err != nil {
		return nil, fmt.Errorf("app: build k8s clientset: %w", err)
	}
	return controller.New(controller.Deps{
		Vault:   vc,
		Backing: &backingProvisioner{cs: cs, namespace: ns},
	}), nil
}

// BuildNode wires the Node service. Fails fast on missing Vault config (C-1).
func BuildNode(cfg Config) (*node.Service, error) {
	if cfg.VaultAddr == "" {
		return nil, fmt.Errorf("app: VAULT_ADDR is required")
	}
	vc, err := vault.New(vault.Config{Address: cfg.VaultAddr, CABundlePath: cfg.VaultCABundle})
	if err != nil {
		return nil, err
	}
	return node.New(node.Deps{
		Keys:   &nodeKeySource{vault: vc},
		Luks:   luks.New(luks.NewExecRunner("cryptsetup")),
		Device: &deviceAdapter{reg: device.NewDefaultRegistry()},
		Mount:  &execMounter{},
	}), nil
}

// newKubeClient returns a Kubernetes clientset, trying in-cluster config first,
// then falling back to the kubeconfig at $KUBECONFIG or ~/.kube/config.
func newKubeClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
		if err != nil {
			return nil, fmt.Errorf("load k8s config: %w", err)
		}
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("new k8s clientset: %w", err)
	}
	return cs, nil
}
