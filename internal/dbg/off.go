//go:build !debug

package dbg

// Enabled is false in production builds. Every `if dbg.Enabled { ... }` block
// is dead code — the compiler removes it entirely, including argument evaluation.
const Enabled = false
