package device

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"k8s.io/client-go/kubernetes"
)

// pvNameRE matches valid Kubernetes PV names (RFC 1123 DNS subdomain subset).
// Path separators and parent-dir sequences are rejected independently below;
// this pattern guards against uppercase, spaces, and other illegal characters.
var pvNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,251}[a-z0-9])?$`)

// validatePVName rejects unsafe or malformed backing PV names.
// Shared by all attach-based resolvers (Longhorn, Ceph RBD, Cinder, …).
func validatePVName(pvName string) error {
	if pvName == "" {
		return fmt.Errorf("device: backingPV is required")
	}
	// Reject path separators and traversal sequences first — these must never
	// reach the regex or path construction below.
	if strings.ContainsAny(pvName, "/\\") || strings.Contains(pvName, "..") {
		return fmt.Errorf("device: invalid backingPV %q", pvName)
	}
	if !pvNameRE.MatchString(pvName) {
		return fmt.Errorf("device: invalid backingPV %q", pvName)
	}
	return nil
}

// blockAttachBase is embedded by all attach-based resolvers to share the
// VolumeAttachment create-and-wait logic. Each resolver supplies its own
// backing CSI driver name (attacher) and device path derivation strategy.
//
// Resolvers for other backing storage providers follow the same pattern:
//
//	Ceph RBD  — after Attach(), read the device path from
//	            VolumeAttachment.Status.AttachmentMetadata["devicePath"]
//	            (populated by rbd.csi.ceph.com ControllerPublishVolume).
//	Cinder    — same, reading AttachmentMetadata["DevicePath"].
type blockAttachBase struct {
	nodeName string
	att      *Attacher
}

func newBlockAttachBase(cs kubernetes.Interface, nodeName string) blockAttachBase {
	if nodeName == "" {
		nodeName = os.Getenv("KUBE_NODE_NAME")
	}
	return blockAttachBase{nodeName: nodeName, att: NewAttacher(cs)}
}

func (b *blockAttachBase) attachPV(ctx context.Context, pvName, csiDriver string) error {
	return b.att.Attach(ctx, AttachRequest{
		PVName:   pvName,
		NodeName: b.nodeName,
		Attacher: csiDriver,
	})
}

// longhornDevice resolves a Longhorn-backed volume by:
//  1. Creating a VolumeAttachment for driver.longhorn.io and waiting for
//     attached=true (Longhorn's external-attacher then starts the engine).
//  2. Returning /dev/longhorn/<backingPV> — Longhorn's predictable block
//     device symlink created once the engine is running on the node.
//
// The caller's WaitForDevice polls until the path exists on the host.
type longhornDevice struct {
	blockAttachBase
}

func (l *longhornDevice) Name() string { return "longhorn" }

func (l *longhornDevice) Resolve(ctx context.Context, params map[string]string) (string, error) {
	pvName := params["backingPV"]
	if err := validatePVName(pvName); err != nil {
		return "", err
	}
	if err := l.attachPV(ctx, pvName, "driver.longhorn.io"); err != nil {
		return "", fmt.Errorf("device: longhorn: %w", err)
	}
	path := filepath.Clean("/dev/longhorn/" + pvName)
	if !strings.HasPrefix(path, "/dev/longhorn/") {
		return "", fmt.Errorf("device: backingPV %q escapes /dev/longhorn/", pvName)
	}
	return path, nil
}

// NewLonghornResolver returns a Resolver for Longhorn-backed volumes.
// nodeName is read from KUBE_NODE_NAME when empty.
func NewLonghornResolver(cs kubernetes.Interface, nodeName string) Resolver {
	return &longhornDevice{blockAttachBase: newBlockAttachBase(cs, nodeName)}
}
