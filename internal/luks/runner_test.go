package luks

import (
	"bytes"
	"context"
	"testing"

	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
)

func TestExecRunnerDeliversKeyOnFD3(t *testing.T) {
	key, err := keyzero.NewBuffer(11)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer key.Zero()
	copy(key.Bytes(), []byte("hello-world"))

	r := NewExecRunner("cat")
	// `cat /dev/fd/3` echoes whatever we wrote to the pipe.
	out, err := r.Run(context.Background(), key, KeyFDPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !bytes.Equal(out, []byte("hello-world")) {
		t.Fatalf("fd-3 payload = %q, want %q", out, "hello-world")
	}
}

func TestExecRunnerNoKeyOmitsExtraFile(t *testing.T) {
	r := NewExecRunner("echo")
	out, err := r.Run(context.Background(), nil, "ok")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(bytes.TrimSpace(out)); got != "ok" {
		t.Fatalf("echo output = %q, want %q", got, "ok")
	}
}
