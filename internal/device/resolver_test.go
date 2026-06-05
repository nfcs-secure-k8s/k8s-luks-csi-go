package device

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistryDispatch(t *testing.T) {
	reg := NewRegistry()
	reg.Register(genericByID{})
	r, err := reg.Get("by-id")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	path, err := r.Resolve(context.Background(), map[string]string{"serial": "abc123"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/dev/disk/by-id/abc123" {
		t.Fatalf("path = %q", path)
	}
}

func TestRegistryUnknownBackend(t *testing.T) {
	reg := NewRegistry()
	if _, err := reg.Get("nope"); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func TestByIDRejectsInjection(t *testing.T) {
	_, err := genericByID{}.Resolve(context.Background(), map[string]string{"serial": "../../etc/passwd"})
	if err == nil {
		t.Fatal("by-id resolver accepted a traversal serial")
	}
}

func TestWaitForDeviceSucceedsWhenStatOK(t *testing.T) {
	calls := 0
	stat := func(string) error {
		calls++
		if calls < 2 {
			return errors.New("not yet")
		}
		return nil
	}
	err := waitForDevice(context.Background(), "/dev/disk/by-id/x", 10*time.Millisecond, time.Second, stat)
	if err != nil {
		t.Fatalf("waitForDevice: %v", err)
	}
}

func TestWaitForDeviceTimesOut(t *testing.T) {
	stat := func(string) error { return errors.New("never") }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := waitForDevice(ctx, "/dev/x", 10*time.Millisecond, time.Hour, stat); err == nil {
		t.Fatal("expected timeout error")
	}
}
