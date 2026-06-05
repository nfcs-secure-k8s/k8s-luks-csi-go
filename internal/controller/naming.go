package controller

import (
	"crypto/sha256"
	"encoding/hex"
)

// backingName derives a collision-resistant backing-PVC name (M-1).
func backingName(institution, volumeName string) string {
	sum := sha256.Sum256([]byte(institution + "\x00" + volumeName))
	return "luks-backing-" + hex.EncodeToString(sum[:16])
}
