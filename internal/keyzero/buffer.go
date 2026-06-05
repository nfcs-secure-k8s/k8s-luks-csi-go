package keyzero

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Buffer holds key material in memory locked against swap.
type Buffer struct {
	data   []byte
	zeroed bool
}

// NewBuffer allocates an mlock'd buffer of n bytes. The caller MUST call
// Zero (typically via defer) to wipe and unlock the pages.
func NewBuffer(n int) (*Buffer, error) {
	if n <= 0 {
		return nil, fmt.Errorf("keyzero: buffer length must be positive, got %d", n)
	}
	data := make([]byte, n)
	if err := unix.Mlock(data); err != nil {
		return nil, fmt.Errorf("keyzero: mlock failed: %w", err)
	}
	return &Buffer{data: data}, nil
}

// Bytes returns the underlying slice. Do not retain references past Zero.
func (b *Buffer) Bytes() []byte { return b.data }

// Zero wipes the buffer and unlocks its pages. Safe to call multiple times.
func (b *Buffer) Zero() {
	if b.zeroed {
		return
	}
	for i := range b.data {
		b.data[i] = 0
	}
	_ = unix.Munlock(b.data)
	b.zeroed = true
}
