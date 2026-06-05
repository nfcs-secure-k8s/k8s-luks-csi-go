package identity

import (
	"context"
	"testing"
)

func TestGetPluginInfoReturnsDriverName(t *testing.T) {
	s := New("v0.1.0")
	info, err := s.GetPluginInfo(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetPluginInfo: %v", err)
	}
	if info.GetName() != "luks.csi.nfcs.io" {
		t.Fatalf("name = %q, want luks.csi.nfcs.io", info.GetName())
	}
	if info.GetVendorVersion() != "v0.1.0" {
		t.Fatalf("version = %q, want v0.1.0", info.GetVendorVersion())
	}
}

func TestProbeReady(t *testing.T) {
	s := New("v0.1.0")
	resp, err := s.Probe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !resp.GetReady().GetValue() {
		t.Fatal("Probe reported not ready")
	}
}
