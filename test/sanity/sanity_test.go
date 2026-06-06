package sanity

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"

	"github.com/nfcs/k8s-luks-csi-go/internal/controller"
	"github.com/nfcs/k8s-luks-csi-go/internal/identity"
	"github.com/nfcs/k8s-luks-csi-go/internal/server"
)

func TestCSISanity(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "csi.sock")
	endpoint := "unix://" + sock

	srv := server.New(server.Config{Endpoint: endpoint, Mode: server.ModeController, Version: "test"})
	srv.WithIdentity(identity.New("test"))
	srv.WithController(controller.New(controller.Deps{
		Vault:   &sanityVault{},
		Backing: &sanityBacking{},
	}))
	go func() { _ = srv.Run() }()
	defer srv.Stop()

	cfg := sanity.NewTestConfig()
	cfg.Address = endpoint
	cfg.TargetPath = filepath.Join(dir, "target")
	cfg.StagingPath = filepath.Join(dir, "staging")
	cfg.TestVolumeParameters = map[string]string{
		"institution":         "bristol",
		"backingStorageClass": "standard",
	}

	sanity.Test(t, cfg)
}

// sanityVault is an in-memory VaultKeyer for sanity tests.
type sanityVault struct {
	keys map[string]bool
}

func (v *sanityVault) EnsureKey(_ context.Context, institution, volume string) error {
	if v.keys == nil {
		v.keys = map[string]bool{}
	}
	v.keys[institution+"/"+volume] = true
	return nil
}

func (v *sanityVault) DeleteKey(_ context.Context, institution, volume string) error {
	if v.keys != nil {
		delete(v.keys, institution+"/"+volume)
	}
	return nil
}

// sanityBacking is an in-memory Backing for sanity tests.
type sanityBacking struct {
	pvs map[string]string
}

func (b *sanityBacking) CreateBackingPVC(_ context.Context, name, _ string) (string, error) {
	if b.pvs == nil {
		b.pvs = map[string]string{}
	}
	pvName := "pv-" + name
	b.pvs[name] = pvName
	return pvName, nil
}

func (b *sanityBacking) DeleteBackingPVC(_ context.Context, name string) error {
	if b.pvs != nil {
		delete(b.pvs, name)
	}
	return nil
}
