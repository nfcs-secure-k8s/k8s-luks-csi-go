package device

import (
	"context"
	"testing"
)

func TestCinderResolverHappyPath(t *testing.T) {
	r := NewCinderResolver()
	path, err := r.Resolve(context.Background(), map[string]string{
		"backingPV":  "pvc-abc",
		"devicePath": "/dev/vdb",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/dev/vdb" {
		t.Fatalf("path = %q, want /dev/vdb", path)
	}
}

func TestCinderResolverRejectsMissingDevicePath(t *testing.T) {
	r := NewCinderResolver()
	if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": "pvc-abc"}); err == nil {
		t.Fatal("expected error when devicePath missing")
	}
}

func TestCinderResolverRejectsInvalidDevicePath(t *testing.T) {
	r := NewCinderResolver()
	bad := []string{
		"../etc/passwd",
		"not-a-dev-path",
		"/proc/self/mem",
		"/dev/../../etc/passwd",
	}
	for _, p := range bad {
		if _, err := r.Resolve(context.Background(), map[string]string{
			"backingPV":  "pvc-abc",
			"devicePath": p,
		}); err == nil {
			t.Fatalf("expected rejection for devicePath %q", p)
		}
	}
}
