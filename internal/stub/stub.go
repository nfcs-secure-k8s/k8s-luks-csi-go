// Package stub anchors the module's direct dependencies so that go mod tidy
// retains them before any real packages are added. This file will be removed
// once the first real package that imports these deps is committed.
package stub

import (
	_ "github.com/container-storage-interface/spec/lib/go/csi"
	_ "github.com/hashicorp/vault/api"
	_ "github.com/hashicorp/vault/api/auth/approle"
	_ "golang.org/x/sys/unix"
	_ "google.golang.org/grpc"
	_ "k8s.io/api/core/v1"
	_ "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/kubernetes"
)
