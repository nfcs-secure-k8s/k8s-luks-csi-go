package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/nfcs/k8s-luks-csi-go/internal/identity"
)

func TestServerServesIdentityOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "csi.sock")
	srv := New(Config{Endpoint: "unix://" + sock, Mode: ModeController, Version: "v0.0.1"})
	srv.WithIdentity(identity.New("v0.0.1"))

	go func() { _ = srv.Run() }()
	defer srv.Stop()

	conn, err := grpc.NewClient("unix://"+sock,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	var info *csi.GetPluginInfoResponse
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		info, err = csi.NewIdentityClient(conn).GetPluginInfo(ctx, &csi.GetPluginInfoRequest{})
		cancel()
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GetPluginInfo: %v", err)
	}
	if info.GetName() != "luks.csi.nfcs.io" {
		t.Fatalf("name = %q", info.GetName())
	}
}
