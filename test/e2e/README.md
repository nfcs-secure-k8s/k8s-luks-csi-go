# e2e Test Harness

End-to-end tests run against a real Lima k3s cluster with Rook-Ceph for block storage
and a **TLS-enabled Vault** (not dev-mode HTTP) so that finding **C-1** (plaintext Vault
transport) is genuinely exercised.

## Prerequisites

- [Lima](https://lima-vm.io/) installed (`brew install lima`)
- `kubectl`, `helm`, `vault` CLI available
- `cosign` for image verification (optional for dev)

## Setup

### 1. Start a k3s cluster via Lima

```shell
limactl start --name=k3s template://k3s
export KUBECONFIG=$(limactl shell k3s -- kubectl config view --raw | kubectl config view --raw --flatten --merge /dev/stdin 2>/dev/null)
```

### 2. Install Rook-Ceph

```shell
helm repo add rook-release https://charts.rook.io/release
helm install --create-namespace --namespace rook-ceph rook-ceph rook-release/rook-ceph
# Wait for Ceph cluster to be healthy (~5 min)
kubectl -n rook-ceph wait --for=condition=Ready cephcluster/rook-ceph --timeout=300s
```

### 3. Deploy a TLS-enabled Vault

Generate a CA and server certificate, then install Vault with TLS:

```shell
# Generate CA + server cert (see vault-tls.md for full commands)
helm repo add hashicorp https://helm.releases.hashicorp.com
helm install vault hashicorp/vault \
  --set server.ha.enabled=false \
  --set server.extraEnvironmentVars.VAULT_CACERT=/vault/userconfig/vault-tls/ca.crt \
  --set server.volumes[0].name=vault-tls \
  --set server.volumes[0].secret.secretName=vault-tls \
  --set server.volumeMounts[0].mountPath=/vault/userconfig/vault-tls \
  --set server.volumeMounts[0].name=vault-tls \
  --set server.volumeMounts[0].readOnly=true

# Initialise and unseal
kubectl exec vault-0 -- vault operator init -key-shares=1 -key-threshold=1
kubectl exec vault-0 -- vault operator unseal <UNSEAL_KEY>
```

### 4. Configure Vault KV v2 + AppRole

```shell
vault secrets enable -path=secret kv-v2
vault auth enable approle

# Per-tenant policy
vault policy write tenant-bristol - <<EOF
path "secret/data/tenants/bristol/*" { capabilities = ["read","create","update"] }
path "secret/metadata/tenants/bristol/*" { capabilities = ["list","delete"] }
EOF

vault write auth/approle/role/bristol \
  token_policies="tenant-bristol" \
  token_ttl=1h \
  token_max_ttl=4h

vault read auth/approle/role/bristol/role-id    # → ROLE_ID
vault write -f auth/approle/role/bristol/secret-id  # → SECRET_ID
```

### 5. Install the LUKS CSI driver

```shell
kubectl create secret generic luks-csi-vault-ca --from-file=ca.pem=<path-to-ca.pem>
helm install luks-csi deploy/helm/ \
  --set vault.addr=https://vault.vault.svc.cluster.local:8200 \
  --set image.digest=sha256:<YOUR_DIGEST>
```

### 6. Create a tenant namespace + node-stage secret

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
