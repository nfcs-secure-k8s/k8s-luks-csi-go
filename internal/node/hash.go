package node

import (
	"crypto/sha256"
	"encoding/hex"
)

// sha256hex16 returns the first 32 hex characters of the SHA-256 hash of s,
// producing a 32-char string that matches the mapper name regex.
func sha256hex16(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}
