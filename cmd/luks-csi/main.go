// Command luks-csi is the LUKS CSI driver entrypoint.
package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/nfcs/k8s-luks-csi-go/internal/app"
	"github.com/nfcs/k8s-luks-csi-go/internal/server"
)

var version = "dev" // set via -ldflags at build time

func main() {
	var (
		endpoint = flag.String("endpoint", "unix:///csi/csi.sock", "CSI gRPC endpoint")
		mode     = flag.String("mode", "all", "controller | node | all")
	)
	flag.Parse()

	setupLogging()

	cfg := app.Config{
		VaultAddr:        os.Getenv("VAULT_ADDR"),
		VaultCABundle:    os.Getenv("VAULT_CACERT"),
		BackingNamespace: envOrDefault("LUKS_CSI_BACKING_NS", "luks-csi-backing"),
	}

	m := server.Mode(*mode)
	srv := server.New(server.Config{
		Endpoint: *endpoint,
		Mode:     m,
		Version:  version,
	})

	if m == server.ModeController || m == server.ModeAll {
		ctrl, err := app.BuildController(cfg)
		if err != nil {
			slog.Error("build controller failed", "err", err)
			os.Exit(1)
		}
		srv.WithController(ctrl)
	}

	if m == server.ModeNode || m == server.ModeAll {
		nodeSvc, err := app.BuildNode(cfg)
		if err != nil {
			slog.Error("build node failed", "err", err)
			os.Exit(1)
		}
		srv.WithNode(nodeSvc)
	}

	slog.Info("starting luks-csi", "mode", *mode, "endpoint", *endpoint, "version", version)
	if err := srv.Run(); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func setupLogging() {
	level := slog.LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		_ = level.UnmarshalText([]byte(v))
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
