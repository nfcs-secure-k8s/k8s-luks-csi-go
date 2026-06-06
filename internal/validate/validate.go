package validate

import (
	"fmt"
	"regexp"
)

// nameRE guards tenant/institution identifiers used in Vault paths: short (≤63 chars).
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

// volumeNameRE guards CSI volume names, which may be up to 253 chars (CSI spec imposes no length limit).
var volumeNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,252}$`)

// mapperRE: a SHA-256-derived mapper name "luks-" + 32 hex chars.
var mapperRE = regexp.MustCompile(`^luks-[0-9a-f]{32}$`)

// Institution validates a tenant identifier used in Vault paths.
func Institution(s string) error {
	if !nameRE.MatchString(s) {
		return fmt.Errorf("validate: invalid institution %q", s)
	}
	return nil
}

// VolumeName validates a CSI volume name. CSI-assigned names may be up to 253 chars.
func VolumeName(s string) error {
	if !volumeNameRE.MatchString(s) {
		return fmt.Errorf("validate: invalid volume name %q", s)
	}
	return nil
}

// MapperName validates a device-mapper target name before use in a
// /dev/mapper/ path or as a cryptsetup argument.
func MapperName(s string) error {
	if !mapperRE.MatchString(s) {
		return fmt.Errorf("validate: invalid mapper name %q", s)
	}
	return nil
}
