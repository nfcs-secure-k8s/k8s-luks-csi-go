# LUKS CSI Driver — Phase 4: Deploy, CI & tests (Tasks 16–19)

> Part of the [Go LUKS CSI Driver plan](00-index.md). Depends on Phases 1–3.

**Covers:** hardened Helm chart, multi-stage distroless image, SHA-pinned CI pipeline, CSI sanity suite + TLS-Vault e2e harness.

---

## Task 16: Hardened Helm chart

Addresses **C-4/H-8** (securityContext), **H-9** (seccomp), **H-10** (resource limits), **H-11** (narrow PVC RBAC), **M-7** (`podInfoOnMount`), **M-8** (NetworkPolicy), and the **H-3** staging-namespace `Role`. The controller runs as a Deployment (non-root, read-only FS); the node runs as a DaemonSet (root only because `cryptsetup`/mount require it, with `IPC_LOCK` for mlock).

**Files:** Create `deploy/helm/Chart.yaml`, `deploy/helm/values.yaml`, and templates under `deploy/helm/templates/`.

- [ ] **Step 1: Create `deploy/helm/Chart.yaml`**

```yaml
apiVersion: v2
name: luks-csi
description: LUKS CSI driver (luks.csi.nfcs.io) with per-tenant Vault key isolation
type: application
version: 0.1.0
appVersion: "0.1.0"
```

- [ ] **Step 2: Create the `CSIDriver` object** (`deploy/helm/templates/csidriver.yaml`) — **M-7**

```yaml
apiVersion: storage.k8s.io/v1
kind: CSIDriver
metadata:
  name: luks.csi.nfcs.io
spec:
  attachRequired: true        # H-3: use external-attacher / VolumeAttachment
  podInfoOnMount: true        # M-7: pass pod identity for mount audit
  volumeLifecycleModes:
    - Persistent
```

- [ ] **Step 3: Create the controller Deployment** (`deploy/helm/templates/controller-deployment.yaml`) — **C-4/H-8/H-9/H-10**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: luks-csi-controller
  namespace: {{ .Release.Namespace }}
spec:
  replicas: 1
  selector:
    matchLabels: { app: luks-csi-controller }
  template:
    metadata:
      labels: { app: luks-csi-controller }
    spec:
      serviceAccountName: luks-csi-controller
      securityContext:
        runAsNonRoot: true
        seccompProfile: { type: RuntimeDefault }   # H-9
      containers:
        - name: luks-csi
          image: "{{ .Values.image.repository }}@{{ .Values.image.digest }}"  # digest-pinned, M-11/M-12
          args: ["-mode=controller", "-endpoint=unix:///csi/csi.sock"]
          env:
            - { name: VAULT_ADDR, value: "{{ .Values.vault.addr }}" }     # must be https (C-1)
            - { name: VAULT_CACERT, value: "/etc/vault/ca.pem" }
            - { name: LOG_LEVEL, value: "{{ .Values.logLevel }}" }
          securityContext:
            runAsNonRoot: true
            readOnlyRootFilesystem: true            # H-8
            allowPrivilegeEscalation: false         # H-8
            capabilities: { drop: ["ALL"] }         # H-8
            seccompProfile: { type: RuntimeDefault } # H-9
          resources:                                 # H-10
            requests: { cpu: 50m, memory: 64Mi }
            limits:   { cpu: 500m, memory: 256Mi }
          volumeMounts:
            - { name: socket-dir, mountPath: /csi }
            - { name: vault-ca, mountPath: /etc/vault, readOnly: true }
        - name: external-provisioner
          image: "{{ .Values.sidecars.provisioner }}"   # SHA/digest-pinned in values
          args: ["--csi-address=/csi/csi.sock", "--leader-election"]
          securityContext:
            runAsNonRoot: true
            readOnlyRootFilesystem: true
            allowPrivilegeEscalation: false
            capabilities: { drop: ["ALL"] }
            seccompProfile: { type: RuntimeDefault }
          resources:
            requests: { cpu: 20m, memory: 32Mi }
            limits:   { cpu: 200m, memory: 128Mi }
          volumeMounts:
            - { name: socket-dir, mountPath: /csi }
        - name: external-attacher                       # H-3
          image: "{{ .Values.sidecars.attacher }}"
          args: ["--csi-address=/csi/csi.sock", "--leader-election"]
          securityContext:
            runAsNonRoot: true
            readOnlyRootFilesystem: true
            allowPrivilegeEscalation: false
            capabilities: { drop: ["ALL"] }
            seccompProfile: { type: RuntimeDefault }
          resources:
            requests: { cpu: 20m, memory: 32Mi }
            limits:   { cpu: 200m, memory: 128Mi }
          volumeMounts:
            - { name: socket-dir, mountPath: /csi }
      volumes:
        - { name: socket-dir, emptyDir: {} }
        - { name: vault-ca, secret: { secretName: luks-csi-vault-ca } }
```

- [ ] **Step 4: Create the node DaemonSet** (`deploy/helm/templates/node-daemonset.yaml`)

The node container needs root + `cryptsetup`/mount privileges and `IPC_LOCK` for `mlock`. It carries **no Vault env credential** (H-1: zero standing access). The registrar sidecar registers the socket.

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: luks-csi-node
  namespace: {{ .Release.Namespace }}
spec:
  selector:
    matchLabels: { app: luks-csi-node }
  template:
    metadata:
      labels: { app: luks-csi-node }
    spec:
      serviceAccountName: luks-csi-node
      hostPID: false
      securityContext:
        seccompProfile: { type: RuntimeDefault }   # H-9 (RuntimeDefault in v1, §11)
      containers:
        - name: luks-csi
          image: "{{ .Values.image.repository }}@{{ .Values.image.digest }}"
          args: ["-mode=node", "-endpoint=unix:///csi/csi.sock"]
          env:
            - { name: VAULT_ADDR, value: "{{ .Values.vault.addr }}" }
            - { name: VAULT_CACERT, value: "/etc/vault/ca.pem" }
            - { name: LOG_LEVEL, value: "{{ .Values.logLevel }}" }
          securityContext:
            privileged: true                 # cryptsetup + mount require it
            capabilities: { add: ["IPC_LOCK"] }  # keyzero mlock (Task 1)
          resources:
            requests: { cpu: 50m, memory: 64Mi }
            limits:   { cpu: 500m, memory: 256Mi }   # H-10
          volumeMounts:
            - { name: socket-dir, mountPath: /csi }
            - { name: kubelet-dir, mountPath: /var/lib/kubelet, mountPropagation: Bidirectional }
            - { name: vault-ca, mountPath: /etc/vault, readOnly: true }
            # M-3: device-specific bind mounts wired at stage time; no blanket /dev.
        - name: node-driver-registrar
          image: "{{ .Values.sidecars.registrar }}"
          args:
            - "--csi-address=/csi/csi.sock"
            - "--kubelet-registration-path=/var/lib/kubelet/plugins/luks.csi.nfcs.io/csi.sock"
          securityContext:
            allowPrivilegeEscalation: false
            capabilities: { drop: ["ALL"] }
            seccompProfile: { type: RuntimeDefault }
          resources:
            requests: { cpu: 10m, memory: 16Mi }
            limits:   { cpu: 100m, memory: 64Mi }
          volumeMounts:
            - { name: socket-dir, mountPath: /csi }
            - { name: registration-dir, mountPath: /registration }
      volumes:
        - { name: socket-dir, hostPath: { path: /var/lib/kubelet/plugins/luks.csi.nfcs.io, type: DirectoryOrCreate } }
        - { name: registration-dir, hostPath: { path: /var/lib/kubelet/plugins_registry, type: Directory } }
        - { name: kubelet-dir, hostPath: { path: /var/lib/kubelet, type: Directory } }
        - { name: vault-ca, secret: { secretName: luks-csi-vault-ca } }
```

- [ ] **Step 5: Create scoped RBAC** (`deploy/helm/templates/rbac.yaml`) — **H-3/H-11**

Controller ClusterRole is narrowed to exactly what `external-provisioner`/`external-attacher` need. The node SA gets **no Vault access and no pod-create**; the only pod-create is a namespaced `Role` in `luks-csi-staging` for map-on-stage backends (H-3).

```yaml
apiVersion: v1
kind: ServiceAccount
metadata: { name: luks-csi-controller, namespace: {{ .Release.Namespace }} }
---
apiVersion: v1
kind: ServiceAccount
metadata: { name: luks-csi-node, namespace: {{ .Release.Namespace }} }
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: luks-csi-controller }
rules:
  - apiGroups: [""]
    resources: ["persistentvolumes"]
    verbs: ["get", "list", "watch", "create", "delete"]   # H-11: no cluster-wide PVC delete
  - apiGroups: [""]
    resources: ["persistentvolumeclaims"]
    verbs: ["get", "list", "watch", "update"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["storageclasses", "csinodes"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["volumeattachments"]
    verbs: ["get", "list", "watch", "create", "delete", "patch"]   # H-3 attach path
  - apiGroups: ["storage.k8s.io"]
    resources: ["volumeattachments/status"]
    verbs: ["patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: luks-csi-controller }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: luks-csi-controller }
subjects:
  - { kind: ServiceAccount, name: luks-csi-controller, namespace: {{ .Release.Namespace }} }
---
# H-3: the ONLY pod-create, scoped to one namespace via a Role (not ClusterRole).
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: luks-csi-staging, namespace: luks-csi-staging }
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch", "create", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: luks-csi-staging, namespace: luks-csi-staging }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: luks-csi-staging }
subjects:
  - { kind: ServiceAccount, name: luks-csi-node, namespace: {{ .Release.Namespace }} }
```

- [ ] **Step 6: Create NetworkPolicies** (`deploy/helm/templates/networkpolicy.yaml`) — **M-8**

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: luks-csi-egress
  namespace: {{ .Release.Namespace }}
spec:
  podSelector:
    matchLabels: { app: luks-csi-controller }
  policyTypes: ["Egress"]
  egress:
    - to: [{ ipBlock: { cidr: "{{ .Values.vault.cidr }}" } }]   # Vault over TLS
      ports: [{ protocol: TCP, port: 8200 }]
    - to: []                                                     # API server
      ports: [{ protocol: TCP, port: 443 }]
    - to: []                                                     # DNS
      ports: [{ protocol: UDP, port: 53 }, { protocol: TCP, port: 53 }]
```

(Repeat for `app: luks-csi-node` with the same egress set.)

- [ ] **Step 7: Create `deploy/helm/values.yaml`**

```yaml
image:
  repository: ghcr.io/nfcs/luks-csi
  digest: "sha256:REPLACE_AT_RELEASE"   # M-11/M-12 digest pin
logLevel: INFO
vault:
  addr: "https://vault.internal:8200"   # C-1 https only
  cidr: "10.0.0.0/24"
sidecars:
  provisioner: "registry.k8s.io/sig-storage/csi-provisioner@sha256:REPLACE"
  attacher:    "registry.k8s.io/sig-storage/csi-attacher@sha256:REPLACE"
  registrar:   "registry.k8s.io/sig-storage/csi-node-driver-registrar@sha256:REPLACE"
```

- [ ] **Step 8: Lint the rendered chart**

Run: `helm lint deploy/helm/ && helm template luks-csi deploy/helm/ > /tmp/rendered.yaml`
Expected: `helm lint` passes; rendered manifests contain `runAsNonRoot: true`, `seccompProfile`, resource `limits`, the `luks-csi-staging` `Role`, and NetworkPolicies.

- [ ] **Step 9: Commit**

```bash
git add deploy/helm/
git commit -m "feat(deploy): hardened Helm chart (securityContext, scoped RBAC, NetworkPolicy)"
```

---

## Task 17: Multi-stage distroless image

Addresses **H-16/M-11/M-12** (image hardening). Multi-stage build → distroless runtime, non-root `USER` for the controller. The node image additionally bundles `cryptsetup` and runs as root (mount/dm-crypt requirement).

**Files:** Create `Dockerfile`, `.dockerignore`.

- [ ] **Step 1: Create `.dockerignore`**

```
.git
test/e2e
docs
*.md
```

- [ ] **Step 2: Create `Dockerfile`**

```dockerfile
# syntax=docker/dockerfile:1
# --- build stage ---
FROM golang:1.23@sha256:REPLACE AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/luks-csi ./cmd/luks-csi

# --- controller runtime: distroless, non-root (controller needs no root) ---
FROM gcr.io/distroless/static-debian12:nonroot@sha256:REPLACE AS controller
COPY --from=build /out/luks-csi /usr/local/bin/luks-csi
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/luks-csi"]

# --- node runtime: needs cryptsetup + mount; minimal Debian, runs as root ---
FROM debian:12-slim@sha256:REPLACE AS node
RUN apt-get update \
 && apt-get install -y --no-install-recommends cryptsetup-bin e2fsprogs \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/luks-csi /usr/local/bin/luks-csi
ENTRYPOINT ["/usr/local/bin/luks-csi"]
```

> **Executor note:** Resolve each `@sha256:REPLACE` to the current digest at build time (`docker buildx imagetools inspect <ref>`). The chart's `image.digest` (Task 16) points at the published manifest. Build the controller target for the Deployment and the node target for the DaemonSet, or publish a single multi-target image and select via `--target`.

- [ ] **Step 3: Build both targets locally**

Run:
```bash
docker build --target controller -t luks-csi:controller-dev .
docker build --target node       -t luks-csi:node-dev .
```
Expected: both build; `docker run --rm luks-csi:controller-dev -h` prints flag usage.

- [ ] **Step 4: Commit**

```bash
git add Dockerfile .dockerignore
git commit -m "feat(image): multi-stage distroless controller + cryptsetup node image"
```

---

## Task 18: CI/CD pipeline — SHA-pinned, signed, scanned

Addresses **C-3** (gitleaks), **H-13/H-14/H-15** (SHA-pinned actions, cosign signing + SBOM + provenance, govulncheck), **M-9/M-10** (least-privilege `permissions`, `GITHUB_TOKEN` via `env`).

**Files:** Create `.github/workflows/ci.yml`, `.golangci.yml`.

- [ ] **Step 1: Create `.golangci.yml`**

```yaml
linters:
  enable:
    - gosec
    - govet
    - staticcheck
    - errcheck
    - ineffassign
    - unused
```

- [ ] **Step 2: Create `.github/workflows/ci.yml`**

```yaml
name: ci
on:
  push: { branches: [main], tags: ["v*"] }
  pull_request: { branches: [main] }

permissions:        # M-9/M-10: least privilege at top level
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4   # NOTE: pin to full SHA at implementation (H-13)
      - uses: actions/setup-go@v5   # NOTE: pin to full SHA (H-13)
        with: { go-version: "1.23" }
      - run: go test ./... -race
      - uses: golangci/golangci-lint-action@v6  # pin to SHA
      - run: |
          go install golang.org/x/vuln/cmd/govulncheck@latest
          govulncheck ./...                      # H-15
      - uses: gitleaks/gitleaks-action@v2        # C-3; pin to SHA
        env: { GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }} }   # M-9 via env

  build-sign:
    needs: test
    if: github.event_name == 'push'
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write       # push image
      id-token: write       # cosign keyless / provenance
    steps:
      - uses: actions/checkout@v4   # pin SHA
      - uses: docker/setup-buildx-action@v3  # pin SHA
      - uses: docker/build-push-action@v6     # pin SHA; multi-arch
        with:
          platforms: linux/amd64,linux/arm64
          push: true
          sbom: true            # H-14 SBOM
          provenance: true      # H-14 SLSA provenance
          tags: ghcr.io/nfcs/luks-csi:${{ github.sha }}
      - uses: sigstore/cosign-installer@v3   # pin SHA
      - run: cosign sign --yes ghcr.io/nfcs/luks-csi:${{ github.sha }}   # H-13

  helm:
    needs: test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4   # pin SHA
      - uses: azure/setup-helm@v4    # pin SHA
      - run: helm lint deploy/helm/
      - run: helm template luks-csi deploy/helm/ | tee /tmp/rendered.yaml
```

> **Executor note (H-13):** Replace every `uses: org/action@vN` with the full commit SHA (`org/action@<40-char-sha>  # vN`) before merging. The `@vN` forms above are placeholders for readability only; an unpinned action is the finding itself. Resolve SHAs via `gh api repos/<org>/<action>/git/refs/tags/<vN>`.

- [ ] **Step 3: Validate the workflow YAML locally**

Run: `python -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml'))" && echo OK`
Expected: `OK` (valid YAML).

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/ci.yml .golangci.yml
git commit -m "ci: SHA-pinned build/sign/scan pipeline with least-privilege permissions"
```

---

## Task 19: CSI sanity suite + e2e harness scaffolding

The official `kubernetes-csi/csi-test` sanity suite validates spec compliance against the driver's Unix socket with no cluster. The e2e harness (Lima k3s + Rook-Ceph + **TLS Vault**) is the integration gate that genuinely exercises **C-1** (the Python harness only ran dev-mode HTTP Vault).

**Files:** Create `test/sanity/sanity_test.go`, `test/e2e/README.md`, `test/e2e/vault-tls.md`.

- [ ] **Step 1: Add the sanity dependency**

Run: `go get github.com/kubernetes-csi/csi-test/v5/pkg/sanity@latest`

- [ ] **Step 2: Write the sanity test** (`test/sanity/sanity_test.go`)

```go
package sanity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"

	"github.com/nfcs/k8s-luks-csi-go/internal/controller"
	"github.com/nfcs/k8s-luks-csi-go/internal/identity"
	"github.com/nfcs/k8s-luks-csi-go/internal/server"
)

func TestCSISanity(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "csi.sock")
	endpoint := "unix://" + sock

	srv := server.New(server.Config{Endpoint: endpoint, Mode: server.ModeController, Version: "test"})
	srv.WithIdentity(identity.New("test"))
	// Controller wired with in-memory fakes (no real Vault/cluster) so the
	// sanity suite can drive Create/Delete/Capabilities deterministically.
	srv.WithController(controller.New(controller.Deps{
		Vault:   sanityVault{},
		Backing: sanityBacking{},
	}))
	go func() { _ = srv.Run() }()
	defer srv.Stop()

	cfg := sanity.NewTestConfig()
	cfg.Address = endpoint
	cfg.TargetPath = filepath.Join(dir, "target")
	cfg.StagingPath = filepath.Join(dir, "staging")
	_ = os.MkdirAll(cfg.TargetPath, 0o755)
	_ = os.MkdirAll(cfg.StagingPath, 0o755)

	sanity.Test(t, cfg)
}
```

> **Executor note:** Provide minimal `sanityVault`/`sanityBacking` fakes in the test file implementing `controller.VaultKeyer`/`controller.Backing` with in-memory maps. Some sanity cases require Node RPCs; if running node sanity, wire `srv` in `ModeAll` with node fakes too. Skip node-mount cases that require root via the sanity config's skip list rather than weakening the driver.

- [ ] **Step 3: Run the sanity suite**

Run: `go test ./test/sanity/ -v`
Expected: PASS for the controller-service sanity cases (Identity + Controller capabilities, Create/Delete validation).

- [ ] **Step 4: Document the e2e harness** (`test/e2e/README.md`, `test/e2e/vault-tls.md`)

Document, with runnable commands: standing up Lima k3s, installing Rook-Ceph, deploying a **TLS-enabled Vault** (generate a CA + server cert, configure the KV v2 mount and a per-tenant AppRole + policy `path "secret/data/tenants/{{identity.entity.aliases...}}/*"`), installing the chart with `vault.addr=https://...`, creating a tenant namespace + node-stage secret, and asserting a pod can write to a LUKS-backed PVC. Note explicitly that the Vault is TLS (not dev-mode HTTP) so **C-1** is exercised.

- [ ] **Step 5: Commit**

```bash
git add test/ go.mod go.sum
git commit -m "test: CSI sanity suite + TLS-Vault e2e harness docs"
```

---

