package keyzero

import (
	"bytes"
	"testing"
)

func TestNewBufferLength(t *testing.T) {
	b, err := NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer b.Zero()
	if got := len(b.Bytes()); got != 32 {
		t.Fatalf("len = %d, want 32", got)
	}
}

func TestZeroWipesContents(t *testing.T) {
	b, err := NewBuffer(8)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	copy(b.Bytes(), []byte("secret!!"))
	b.Zero()
	if !bytes.Equal(b.Bytes(), make([]byte, 8)) {
		t.Fatalf("buffer not zeroed: %x", b.Bytes())
	}
}

func TestZeroIsIdempotent(t *testing.T) {
	b, err := NewBuffer(4)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	b.Zero()
	b.Zero() // must not panic or double-munlock
}

func TestNewBufferRejectsNonPositive(t *testing.T) {
	if _, err := NewBuffer(0); err == nil {
		t.Fatal("expected error for zero-length buffer")
	}
}
