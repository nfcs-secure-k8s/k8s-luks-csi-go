package device

import (
	"fmt"
	"regexp"
	"strings"
)

// pvNameRE matches valid Kubernetes PV names (RFC 1123 DNS subdomain subset).
// Path separators and parent-dir sequences are rejected separately below; this
// pattern guards against uppercase, spaces, and other illegal characters.
var pvNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,251}[a-z0-9])?$`)

// validatePVName rejects unsafe or malformed backing PV names.
// Shared by the attach-based resolvers (Longhorn, Ceph RBD).
func validatePVName(pvName string) error {
	if pvName == "" {
		return fmt.Errorf("device: backingPV is required")
	}
	// Reject path separators and traversal sequences before the regex/path use.
	if strings.ContainsAny(pvName, "/\\") || strings.Contains(pvName, "..") {
		return fmt.Errorf("device: invalid backingPV %q", pvName)
	}
	if !pvNameRE.MatchString(pvName) {
		return fmt.Errorf("device: invalid backingPV %q", pvName)
	}
	return nil
}
