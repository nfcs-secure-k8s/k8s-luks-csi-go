package luks

import (
	"context"
	"strings"
	"testing"

	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
)

// fakeRunner records calls and returns canned output/errors keyed by the
// first argument (the cryptsetup subcommand).
type fakeRunner struct {
	calls   [][]string
	gotKey  bool
	outputs map[string][]byte
	errs    map[string]error
}

func (f *fakeRunner) Run(_ context.Context, key *keyzero.Buffer, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if key != nil {
		f.gotKey = true
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	return f.outputs[sub], f.errs[sub]
}

func newKey(t *testing.T) *keyzero.Buffer {
	t.Helper()
	k, err := keyzero.NewBuffer(8)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	copy(k.Bytes(), []byte("testkey1"))
	return k
}

func TestFormatUsesArgon2idAndKeyFD(t *testing.T) {
	fr := &fakeRunner{}
	l := New(fr)
	key := newKey(t)
	defer key.Zero()

	if err := l.Format(context.Background(), "/dev/loopX", key); err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !fr.gotKey {
		t.Fatal("Format did not pass key on fd")
	}
	joined := strings.Join(fr.calls[0], " ")
	if !strings.Contains(joined, "--pbkdf argon2id") {
		t.Fatalf("Format did not force argon2id: %q", joined)
	}
	if !strings.Contains(joined, "--key-file="+KeyFDPath) {
		t.Fatalf("Format did not use fd key-file: %q", joined)
	}
	if strings.Contains(joined, "luksFormat --type luks1") {
		t.Fatal("Format must never request luks1")
	}
}

func TestOpenValidatesMapperName(t *testing.T) {
	l := New(&fakeRunner{})
	key := newKey(t)
	defer key.Zero()
	// Injection attempt as mapper name must be rejected before exec.
	err := l.Open(context.Background(), "/dev/loopX", "evil; rm -rf /", key)
	if err == nil {
		t.Fatal("Open accepted an invalid mapper name")
	}
}

func TestOpenIsIdempotentWhenMapperExists(t *testing.T) {
	// status returns non-empty output => mapper already active => Open is a no-op.
	fr := &fakeRunner{outputs: map[string][]byte{"status": []byte("is active")}}
	l := New(fr)
	key := newKey(t)
	defer key.Zero()

	name := "luks-0123456789abcdef0123456789abcdef"
	if err := l.Open(context.Background(), "/dev/loopX", name, key); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, c := range fr.calls {
		if len(c) > 0 && c[0] == "luksOpen" {
			t.Fatal("Open ran luksOpen despite active mapper")
		}
	}
}

func TestIsLUKSReturnsTrueOnZeroExit(t *testing.T) {
	fr := &fakeRunner{} // isLuks subcommand returns nil error => true
	l := New(fr)
	ok, err := l.IsLUKS(context.Background(), "/dev/loopX")
	if err != nil {
		t.Fatalf("IsLUKS: %v", err)
	}
	if !ok {
		t.Fatal("IsLUKS = false, want true on zero exit")
	}
}
