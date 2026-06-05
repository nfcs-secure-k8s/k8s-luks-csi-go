package controller

import (
	"crypto/sha256"
	"encoding/hex"
)

// backingName derives a collision-resistant backing-PVC name (M-1).
func backingName(namespace, volumeName string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + volumeName))
	return "luks-backing-" + hex.EncodeToString(sum[:16])
}
