package validate

import (
	"strings"
	"testing"
)

func TestInstitution(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"simple", "bristol", false},
		{"dots-dashes", "uni-bristol.ac.uk", false},
		{"alnum", "inst01", false},
		{"empty", "", true},
		{"leading-dot", ".bristol", true},
		{"traversal", "../secret/admin", true},
		{"slash", "a/b", true},
		{"too-long", strings.Repeat("a", 64), true},
		{"newline", "bristol\nx", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Institution(tt.in); (err != nil) != tt.wantErr {
				t.Fatalf("Institution(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestVolumeName(t *testing.T) {
	if err := VolumeName("pvc-abc123"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	if err := VolumeName("../escape"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestMapperName(t *testing.T) {
	if err := MapperName("luks-0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("valid mapper name rejected: %v", err)
	}
	for _, b := range []string{
		"luks-XYZ",
		"luks-0123456789abcdef0123456789abcde",   // 31 hex
		"luks-0123456789abcdef0123456789abcdef0", // 33 hex
		"evil; rm -rf /",
		"0123456789abcdef0123456789abcdef", // no prefix
	} {
		if err := MapperName(b); err == nil {
			t.Fatalf("invalid mapper name accepted: %q", b)
		}
	}
}
