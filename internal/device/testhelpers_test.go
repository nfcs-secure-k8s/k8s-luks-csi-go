package device

// fakeFinder returns /dev/rbd0 for all pool/image queries.
// Used by ceph_test.go.
func fakeFinder(_, _ string) (string, error) {
	return "/dev/rbd0", nil
}
