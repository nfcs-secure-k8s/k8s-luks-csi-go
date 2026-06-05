package vault

import (
	"context"
	"testing"
)

func makeTryOpen(correct string) func([]byte) bool {
	return func(k []byte) bool { return string(k) == correct }
}

func TestFindAuthenticatingVersionWalksBackward(t *testing.T) {
	fk := &fakeKV{versions: map[string][][]byte{
		"tenants/bristol/pvc-1": {[]byte("correct1"), []byte("newkey02"), []byte("newkey03")},
	}}
	c := &Client{kv: fk}

	buf, ver, err := c.FindAuthenticatingVersion(
		context.Background(), "bristol", "pvc-1", makeTryOpen("correct1"))
	if err != nil {
		t.Fatalf("FindAuthenticatingVersion: %v", err)
	}
	defer buf.Zero()
	if ver != 1 {
		t.Fatalf("authenticating version = %d, want 1", ver)
	}
	if string(buf.Bytes()) != "correct1" {
		t.Fatalf("returned key = %q, want correct1", buf.Bytes())
	}
}

func TestFindAuthenticatingVersionExhausted(t *testing.T) {
	fk := &fakeKV{versions: map[string][][]byte{
		"tenants/bristol/pvc-1": {[]byte("nope0001"), []byte("nope0002")},
	}}
	c := &Client{kv: fk}
	_, _, err := c.FindAuthenticatingVersion(
		context.Background(), "bristol", "pvc-1", makeTryOpen("absent"))
	if err == nil {
		t.Fatal("expected error when no version authenticates")
	}
}

func TestEnsureKeyIdempotent(t *testing.T) {
	fk := &fakeKV{versions: map[string][][]byte{}}
	c := &Client{kv: fk, genKey: func(n int) ([]byte, error) {
		return []byte("genkey00"), nil
	}}
	if err := c.EnsureKey(context.Background(), "bristol", "pvc-1"); err != nil {
		t.Fatalf("EnsureKey create: %v", err)
	}
	if err := c.EnsureKey(context.Background(), "bristol", "pvc-1"); err != nil {
		t.Fatalf("EnsureKey idempotent: %v", err)
	}
	if got := len(fk.versions["tenants/bristol/pvc-1"]); got != 1 {
		t.Fatalf("EnsureKey created %d versions, want 1", got)
	}
}
