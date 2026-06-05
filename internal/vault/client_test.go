package vault

import (
	"context"
	"strings"
	"testing"
)

func TestNewRejectsPlaintextHTTP(t *testing.T) {
	_, err := New(Config{Address: "http://vault.internal:8200", CABundlePath: "/tmp/ca.pem"})
	if err == nil {
		t.Fatal("New accepted a plaintext http:// address")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("error should mention https requirement: %v", err)
	}
}

func TestNewRejectsMissingCABundle(t *testing.T) {
	if _, err := New(Config{Address: "https://vault.internal:8200"}); err == nil {
		t.Fatal("New accepted an https address with no CA bundle")
	}
}

func TestKeyPathScopedAndValidated(t *testing.T) {
	p, err := keyPath("bristol", "pvc-abc123")
	if err != nil {
		t.Fatalf("keyPath: %v", err)
	}
	if p != "tenants/bristol/pvc-abc123" {
		t.Fatalf("keyPath = %q, want tenants/bristol/pvc-abc123", p)
	}
	if _, err := keyPath("../admin", "pvc-abc123"); err == nil {
		t.Fatal("keyPath accepted a traversal institution")
	}
	if _, err := keyPath("bristol", "../escape"); err == nil {
		t.Fatal("keyPath accepted a traversal volume")
	}
}

// fakeKV is an in-memory KV v2 stand-in.
type fakeKV struct {
	versions map[string][][]byte // path -> []version payloads (1-indexed)
	putErr   error
}

func (f *fakeKV) Get(_ context.Context, path string) ([]byte, int, error) {
	v := f.versions[path]
	if len(v) == 0 {
		return nil, 0, errNotFound
	}
	return v[len(v)-1], len(v), nil
}
func (f *fakeKV) GetVersion(_ context.Context, path string, version int) ([]byte, error) {
	v := f.versions[path]
	if version < 1 || version > len(v) {
		return nil, errNotFound
	}
	return v[version-1], nil
}
func (f *fakeKV) Put(_ context.Context, path string, key []byte) (int, error) {
	if f.putErr != nil {
		return 0, f.putErr
	}
	f.versions[path] = append(f.versions[path], key)
	return len(f.versions[path]), nil
}
func (f *fakeKV) DeleteAll(_ context.Context, path string) error {
	delete(f.versions, path)
	return nil
}

func TestReadKeyReturnsCurrentVersion(t *testing.T) {
	fk := &fakeKV{versions: map[string][][]byte{
		"tenants/bristol/pvc-1": {[]byte("oldkey00"), []byte("newkey01")},
	}}
	c := &Client{kv: fk}
	buf, err := c.ReadKey(context.Background(), "bristol", "pvc-1")
	if err != nil {
		t.Fatalf("ReadKey: %v", err)
	}
	defer buf.Zero()
	if string(buf.Bytes()) != "newkey01" {
		t.Fatalf("ReadKey returned %q, want current version newkey01", buf.Bytes())
	}
}
