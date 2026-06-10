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
	"github.com/nfcs/k8s-luks-csi-go/internal/staging"
	"github.com/nfcs/k8s-luks-csi-go/internal/vault"
)

// Config holds runtime configuration sourced from env/flags.
type Config struct {
	VaultAddr        string
	VaultCABundle    string
	BackingNamespace string // namespace for backing PVCs (default: luks-csi-backing)
	StagingNamespace string // namespace for staging pods (default: luks-csi-staging)
	NodeName         string // Kubernetes node name (default: KUBE_NODE_NAME env var)
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
	cs, err := newKubeClient()
	if err != nil {
		return nil, fmt.Errorf("app: build k8s clientset: %w", err)
	}

	backingNS := cfg.BackingNamespace
	if backingNS == "" {
		backingNS = "luks-csi-backing"
	}
	stagingNS := cfg.StagingNamespace
	if stagingNS == "" {
		stagingNS = "luks-csi-staging"
	}
	nodeName := cfg.NodeName
	if nodeName == "" {
		nodeName = os.Getenv("KUBE_NODE_NAME")
	}

	stagingMgr := staging.New(cs, staging.Config{
		NodeName:  nodeName,
		StagingNS: stagingNS,
		BackingNS: backingNS,
	})

	reg := device.NewDefaultRegistry()
	reg.Register(device.NewLonghornResolver())
	reg.Register(device.NewCephResolver(cs))
	reg.Register(device.NewCinderResolver())

	return node.New(node.Deps{
		Keys:   &nodeKeySource{vault: vc},
		Luks:   luks.New(luks.NewExecRunner("cryptsetup")),
		Device: &deviceAdapter{reg: reg},
		Mount:  &execMounter{},
		Stager: stagingMgr,
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
