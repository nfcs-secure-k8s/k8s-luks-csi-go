//go:build debug

package dbg

// Enabled is true in debug builds (go build -tags debug).
// Debug logs may contain sensitive identifiers and must never be used in production.
const Enabled = true
