# e2e Test Harness

End-to-end tests run against a real Lima k3s cluster with Rook-Ceph for block storage
and a **TLS-enabled Vault** (not dev-mode HTTP) so that finding **C-1** (plaintext Vault
transport) is genuinely exercised.

## Prerequisites

- [Lima](https://lima-vm.io/) installed (`brew install lima`)
- `kubectl`, `helm`, `vault` CLI available
- `podman` for building images

## Setup

### 1. Start a k3s cluster via Lima

```shell
limactl start --name=k3s template://k3s
```

Export the kubeconfig to a file (KUBECONFIG must be a file path, not raw YAML):

```shell
limactl shell k3s -- kubectl config view --raw > /tmp/k3s.yaml
export KUBECONFIG=/tmp/k3s.yaml
kubectl get nodes   # should show lima-k3s Ready
```

> **Note:** This export only lasts for your current shell session. Re-run it after
> opening a new terminal or if Lima restarts.

### 2. Install Rook-Ceph

```shell
helm repo add rook-release https://charts.rook.io/release
helm install --create-namespace --namespace rook-ceph rook-ceph rook-release/rook-ceph
```

The helm chart installs the operator only. You must also create a `CephCluster`
resource. The Lima k3s VM has a clean 20 GiB loop device (`loop10`) suitable for
Ceph — apply this single-node config:

```shell
kubectl apply -f - <<'EOF'
apiVersion: ceph.rook.io/v1
kind: CephCluster
metadata:
  name: rook-ceph
  namespace: rook-ceph
spec:
  cephVersion:
    image: quay.io/ceph/ceph:v19
  dataDirHostPath: /var/lib/rook
  mon:
    count: 1
    allowMultiplePerNode: true
  mgr:
    count: 1
    allowMultiplePerNode: true
  dashboard:
    enabled: false
  crashCollector:
    disable: true
  logCollector:
    enabled: false
  storage:
    useAllNodes: false
    useAllDevices: false
    nodes:
      - name: "lima-k3s"
        devices:
          - name: "loop10"
EOF
```

Wait for the cluster to become ready (~5 min):

```shell
kubectl -n rook-ceph wait --for=condition=Ready cephcluster/rook-ceph --timeout=300s
```

`HEALTH_WARN` is normal on a single-node cluster (no replication redundancy). The
condition only checks `Ready` phase.

### 3. Deploy a TLS-enabled Vault

#### 3a. Generate TLS certificates

Use a plain extension file rather than process substitution to avoid shell
compatibility issues:

```shell
# CA
openssl genrsa -out ca.key 2048
openssl req -x509 -new -nodes -key ca.key -sha256 -days 365 \
  -subj "/CN=vault-ca" -out ca.crt

# Server key + CSR
openssl genrsa -out vault.key 2048
openssl req -new -key vault.key \
  -subj "/CN=vault.vault.svc.cluster.local" -out vault.csr

# Extension file — must include the 127.0.0.1 IP SAN for port-forwarding to work
echo "subjectAltName=DNS:vault.vault.svc.cluster.local,DNS:vault,IP:127.0.0.1" \
  > vault-san.cnf

# Sign
openssl x509 -req -in vault.csr -CA ca.crt -CAkey ca.key \
  -CAcreateserial -out vault.crt -days 365 -sha256 \
  -extfile vault-san.cnf

# Verify the IP SAN is present before continuing
openssl x509 -in vault.crt -noout -ext subjectAltName
# Expected output includes: IP Address:127.0.0.1
```

#### 3b. Create the TLS secret

```shell
kubectl create namespace vault
kubectl -n vault create secret generic vault-tls \
  --from-file=ca.crt=ca.crt \
  --from-file=tls.crt=vault.crt \
  --from-file=tls.key=vault.key
```

#### 3c. Install Vault with TLS listener enabled

The helm chart does not enable TLS by default — you must override the standalone
listener config. Save this values file:

```shell
cat > /tmp/vault-values.yaml << 'EOF'
server:
  ha:
    enabled: false
  extraEnvironmentVars:
    VAULT_CACERT: /vault/userconfig/vault-tls/ca.crt
  volumes:
    - name: vault-tls
      secret:
        secretName: vault-tls
  volumeMounts:
    - mountPath: /vault/userconfig/vault-tls
      name: vault-tls
      readOnly: true
  standalone:
    config: |
      ui = false
      listener "tcp" {
        tls_disable     = 0
        address         = "[::]:8200"
        cluster_address = "[::]:8201"
        tls_cert_file   = "/vault/userconfig/vault-tls/tls.crt"
        tls_key_file    = "/vault/userconfig/vault-tls/tls.key"
        tls_client_ca_file = "/vault/userconfig/vault-tls/ca.crt"
      }
      storage "file" {
        path = "/vault/data"
      }
EOF
```

```shell
helm repo add hashicorp https://helm.releases.hashicorp.com
helm install vault hashicorp/vault --namespace vault -f /tmp/vault-values.yaml
```

> **zsh note:** Quote any `--set` arguments containing `[` brackets, e.g.
> `--set 'server.volumes[0].name=vault-tls'`, or use a values file as above.

#### 3d. Initialise and unseal

Port-forward Vault in a separate terminal and keep it running:

```shell
kubectl -n vault port-forward svc/vault 8200:8200
```

In your main terminal:

```shell
export VAULT_ADDR=https://127.0.0.1:8200
export VAULT_CACERT=/path/to/ca.crt   # full path to the ca.crt you generated

# Initialise — prints Unseal Key and Root Token; save both
kubectl -n vault exec vault-0 -- vault operator init \
  -key-shares=1 -key-threshold=1

# Unseal
vault operator unseal <UNSEAL_KEY>

export VAULT_TOKEN=<ROOT_TOKEN>
```

> **Note:** Vault always starts sealed after a pod restart. Re-run `vault operator unseal`
> any time the `vault-0` pod restarts.

### 4. Configure Vault KV v2 + AppRole

```shell
vault secrets enable -path=secret kv-v2
vault auth enable approle

# Per-tenant policy
vault policy write tenant-bristol - <<'EOF'
path "secret/data/tenants/bristol/*" { capabilities = ["read","create","update"] }
path "secret/metadata/tenants/bristol/*" { capabilities = ["list","delete"] }
EOF

vault write auth/approle/role/bristol \
  token_policies="tenant-bristol" \
  token_ttl=1h \
  token_max_ttl=4h

vault read auth/approle/role/bristol/role-id       # note the role_id value
vault write -f auth/approle/role/bristol/secret-id # note the secret_id value
```

### 5. Build and load the CSI driver image

The Dockerfile has two targets with different runtime requirements:

- `controller` — distroless nonroot; handles CSI controller RPCs (no cryptsetup needed)
- `node` — debian:12-slim with cryptsetup; runs on every node to mount LUKS volumes

Build and load both into Lima's containerd (`-n k8s.io` is the namespace k3s uses):

```shell
WORKTREE=/Users/hd25646/Documents/NFCS-secure-k8s/k8s-luks-csi-go/.claude/worktrees/feat+go-luks-csi-driver

# Node image
podman build --target node -t ghcr.io/nfcs/luks-csi:dev "$WORKTREE"
podman save ghcr.io/nfcs/luks-csi:dev \
  | limactl shell k3s -- sudo ctr -n k8s.io images import -

# Controller image
podman build --target controller -t ghcr.io/nfcs/luks-csi-ctrl:dev "$WORKTREE"
podman save ghcr.io/nfcs/luks-csi-ctrl:dev \
  | limactl shell k3s -- sudo ctr -n k8s.io images import -
```

### 6. Create the Vault CA secret and install the LUKS CSI driver

```shell
kubectl create secret generic luks-csi-vault-ca --from-file=ca.pem=ca.crt

helm install luks-csi deploy/helm/ \
  --set vault.addr=https://vault.vault.svc.cluster.local:8200 \
  --set image.tag=dev \
  --set image.controllerRepository=ghcr.io/nfcs/luks-csi-ctrl \
  --set image.controllerTag=dev
```

Wait for both pods to be ready:

```shell
kubectl get pods -w
# Expected: luks-csi-controller-* 3/3 Running, luks-csi-node-* 2/2 Running
```

If you need to iterate on the image, rebuild with podman, re-import into Lima, then
run `helm upgrade` with the same flags.

### 7. Create a tenant namespace and node-stage secret

```shell
kubectl create namespace bristol
kubectl -n bristol create secret generic vault-cred \
  --from-literal=roleId=<ROLE_ID> \
  --from-literal=secretId=<SECRET_ID>
```

## Running the Tests

```shell
go test ./test/e2e/ -v -timeout 10m
```

## Assertions

The test suite verifies:
1. A pod in namespace `bristol` can mount a LUKS-backed PVC and write/read data.
2. The node SA has **no standing Vault access** — attempt a `vault read` with the node SA token and confirm it is denied (validates **H-1**).
3. The Vault connection uses TLS (validates **C-1** — this harness does NOT use dev-mode HTTP Vault).
