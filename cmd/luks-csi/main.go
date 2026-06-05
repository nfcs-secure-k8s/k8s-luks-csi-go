// Command luks-csi is the LUKS CSI driver entrypoint.
package main

import (
	"flag"
	"log/slog"
	"os"

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

	srv := server.New(server.Config{
		Endpoint: *endpoint,
		Mode:     server.Mode(*mode),
		Version:  version,
	})

	// NOTE: dependency wiring (vault.New, device registry, luks runners,
	// client-go in-cluster config) is assembled here per mode and attached
	// via srv.WithController(...) / srv.WithNode(...). See Task 15.

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
