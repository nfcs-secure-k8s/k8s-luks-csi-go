package device

import (
	"context"
	"strings"
	"testing"
)

func TestLonghornResolverHappyPath(t *testing.T) {
	r := NewLonghornResolver()
	path, err := r.Resolve(context.Background(), map[string]string{
		"backingPV": "pvc-7addc49f-e822-4465-a416-284d924dab4d",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	const want = "/dev/longhorn/pvc-7addc49f-e822-4465-a416-284d924dab4d"
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestLonghornResolverRejectsInvalidBackingPV(t *testing.T) {
	r := NewLonghornResolver()
	bad := []string{
		"",
		"../../etc/passwd",
		"pvc/bad",
		"pvc..bad",
		"PVC-UPPERCASE",
		"pvc-" + strings.Repeat("a", 260),
	}
	for _, name := range bad {
		if _, err := r.Resolve(context.Background(), map[string]string{"backingPV": name}); err == nil {
			t.Fatalf("expected rejection for backingPV %q", name)
		}
	}
}
