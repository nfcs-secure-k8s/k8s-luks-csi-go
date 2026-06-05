package server

// TestMain sets TMPDIR to a short path before running tests so that Unix
// socket paths derived from t.TempDir() stay within the OS sun_path limit
// (104 bytes on macOS, 108 on Linux). On macOS the default TMPDIR is under
// /var/folders/... which, combined with a long test name, can exceed the limit.

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Override TMPDIR so t.TempDir() paths are short enough for Unix sockets
	// on macOS (sun_path limit = 104 bytes).
	if _, set := os.LookupEnv("TMPDIR_ORIGINAL"); !set {
		if err := os.Setenv("TMPDIR", "/tmp"); err == nil {
			_ = os.Setenv("TMPDIR_ORIGINAL", "1")
		}
	}
	os.Exit(m.Run())
}
