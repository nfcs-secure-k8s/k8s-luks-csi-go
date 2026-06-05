package server

import (
	"fmt"
	"net"
	"os"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"

	"github.com/nfcs/k8s-luks-csi-go/internal/controller"
	"github.com/nfcs/k8s-luks-csi-go/internal/identity"
	"github.com/nfcs/k8s-luks-csi-go/internal/node"
)

type Mode string

const (
	ModeController Mode = "controller"
	ModeNode       Mode = "node"
	ModeAll        Mode = "all"
)

type Config struct {
	Endpoint string // unix://<path> or tcp://<addr>
	Mode     Mode
	Version  string
}

type Server struct {
	cfg        Config
	grpc       *grpc.Server
	identity   *identity.Service
	controller *controller.Service
	node       *node.Service
}

func New(cfg Config) *Server {
	return &Server{cfg: cfg, grpc: grpc.NewServer()}
}

func (s *Server) WithIdentity(i *identity.Service) *Server     { s.identity = i; return s }
func (s *Server) WithController(c *controller.Service) *Server { s.controller = c; return s }
func (s *Server) WithNode(n *node.Service) *Server             { s.node = n; return s }

func (s *Server) Run() error {
	network, addr, err := parseEndpoint(s.cfg.Endpoint)
	if err != nil {
		return err
	}
	if network == "unix" {
		_ = os.Remove(addr)
	}
	lis, err := net.Listen(network, addr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", s.cfg.Endpoint, err)
	}

	if s.identity == nil {
		s.identity = identity.New(s.cfg.Version)
	}
	csi.RegisterIdentityServer(s.grpc, s.identity)

	if s.cfg.Mode == ModeController || s.cfg.Mode == ModeAll {
		if s.controller != nil {
			csi.RegisterControllerServer(s.grpc, s.controller)
		}
	}
	if s.cfg.Mode == ModeNode || s.cfg.Mode == ModeAll {
		if s.node != nil {
			csi.RegisterNodeServer(s.grpc, s.node)
		}
	}
	return s.grpc.Serve(lis)
}

func (s *Server) Stop() { s.grpc.GracefulStop() }

func parseEndpoint(ep string) (network, addr string, err error) {
	switch {
	case strings.HasPrefix(ep, "unix://"):
		return "unix", strings.TrimPrefix(ep, "unix://"), nil
	case strings.HasPrefix(ep, "tcp://"):
		return "tcp", strings.TrimPrefix(ep, "tcp://"), nil
	default:
		return "", "", fmt.Errorf("server: unsupported endpoint scheme %q", ep)
	}
}
