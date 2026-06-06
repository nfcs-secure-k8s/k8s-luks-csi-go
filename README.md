# luks-csi

A Kubernetes CSI driver that transparently encrypts persistent volumes with LUKS2, using HashiCorp Vault for per-tenant key management.

Each tenant gets an isolated Vault AppRole. Encryption keys are fetched at stage time, held in mlock'd memory, and zeroed immediately after use — they are never written to disk. The driver name is `luks.csi.nfcs.io`.

## How it works

```
Pod → PVC → StorageClass (luks.csi.nfcs.io)
               │
               ├─ Controller: provisions a backing block PVC + generates a
               │              LUKS key in Vault under tenants/<institution>/<volume>
               │
               └─ Node (stage): fetches key from Vault via tenant AppRole,
                                runs cryptsetup luksFormat/luksOpen (LUKS2,
                                argon2id KDF), mounts the mapper device,
                                zeroes the key buffer
```

Key material flows from Vault to `cryptsetup` through an OS pipe (`/dev/fd/3`), never touching the filesystem.

## Requirements

- Kubernetes 1.27+
- HashiCorp Vault 1.12+ with KV v2 enabled, reachable over **HTTPS** (HTTP is rejected at startup)
- A block storage backend with a `StorageClass` (Ceph RBD, Longhorn, cloud volumes, etc.)
- `cryptsetup` 2.x on each node (supplied by the node container image)

## Installation

### 1. Add the Vault CA secret

The driver verifies Vault's TLS certificate. Create a secret with your CA bundle in the namespace where the driver will run (default: `luks-csi`):

```shell
kubectl create namespace luks-csi
kubectl -n luks-csi create secret generic luks-csi-vault-ca \
  --from-file=ca.pem=/path/to/your/vault-ca.pem
```

### 2. Install via Helm

```shell
helm install luks-csi deploy/helm/ \
  --namespace luks-csi \
  --set vault.addr=https://vault.example.com:8200 \
  --set vault.cidr=10.0.0.0/16 \
  --set image.digest=sha256:<DIGEST>
```

Pin `image.digest` to a specific release digest. The chart requires it — floating tags are not supported.

Key Helm values:

| Value | Default | Description |
|---|---|---|
| `vault.addr` | `https://vault.internal:8200` | Vault address (must be `https://`) |
| `vault.cidr` | `10.0.0.0/24` | CIDR used in the egress NetworkPolicy to allow traffic to Vault |
| `image.digest` | `sha256:REPLACE_AT_RELEASE` | Image digest to pin (required) |
| `logLevel` | `INFO` | Log level (`DEBUG`, `INFO`, `WARN`, `ERROR`) |
| `sidecars.provisioner` | `...@sha256:REPLACE` | external-provisioner image digest |
| `sidecars.attacher` | `...@sha256:REPLACE` | external-attacher image digest |
| `sidecars.registrar` | `...@sha256:REPLACE` | node-driver-registrar image digest |

### 3. Configure Vault

Enable KV v2 and AppRole auth, then create a per-tenant policy and role:

```shell
vault secrets enable -path=secret kv-v2
vault auth enable approle

# Policy for tenant "bristol"
vault policy write tenant-bristol - <<EOF
path "secret/data/tenants/bristol/*"     { capabilities = ["read","create","update"] }
path "secret/metadata/tenants/bristol/*" { capabilities = ["list","delete"] }
EOF

vault write auth/approle/role/bristol \
  token_policies="tenant-bristol" \
  token_ttl=1h token_max_ttl=4h

ROLE_ID=$(vault read -field=role_id auth/approle/role/bristol/role-id)
SECRET_ID=$(vault write -f -field=secret_id auth/approle/role/bristol/secret-id)
```

### 4. Create a StorageClass

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: luks-ceph-block
provisioner: luks.csi.nfcs.io
parameters:
  institution: bristol          # Vault tenant — maps to tenants/bristol/...
  backingStorageClass: rook-ceph-block
  deletionPolicy: Retain        # Retain (default) or Delete
reclaimPolicy: Retain
volumeBindingMode: Immediate
```

### 5. Create a node-stage secret

The node service has no standing Vault access. Each workload namespace holds a secret with the tenant AppRole credentials that are passed to the driver at stage time:

```shell
kubectl -n <workload-namespace> create secret generic vault-cred \
  --from-literal=roleId=$ROLE_ID \
  --from-literal=secretId=$SECRET_ID
```

Reference it in the PVC's `StorageClass` or via a `VolumeAttributesClass` (see Kubernetes CSI secrets documentation for `csi.storage.k8s.io/node-stage-secret-*` annotations).

### 6. Create a PVC

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: encrypted-data
  namespace: bristol
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: luks-ceph-block
  resources:
    requests:
      storage: 10Gi
```

## Building from source

```shell
# Run tests
go test ./... -race

# Build the binary
go build -o luks-csi ./cmd/luks-csi

# Build container images (requires Docker with BuildKit)
docker build --target controller -t luks-csi:controller .
docker build --target node       -t luks-csi:node .
```

## Architecture

| Package | Responsibility |
|---|---|
| `internal/keyzero` | mlock'd byte buffers that zero on `defer` (keys never swapped to disk) |
| `internal/csierr` | Error boundary: full detail logged internally, callers receive only a gRPC code + generic message |
| `internal/validate` | Shared validators for institution names, volume names, and device-mapper names |
| `internal/luks` | `cryptsetup` wrapper: fd-based key passing, LUKS2 only, argon2id KDF enforced |
| `internal/vault` | TLS-required Vault client: per-tenant KV paths, AppRole login, token renewal, version-walk key rotation |
| `internal/device` | Resolver registry (by-id, Longhorn, Ceph RBD) + VolumeAttachment-based attach |
| `internal/identity` | CSI Identity service |
| `internal/controller` | CSI Controller service: CreateVolume / DeleteVolume with collision-resistant backing names |
| `internal/node` | CSI Node service: full stage/unstage orchestration |
| `internal/server` | gRPC bootstrap over Unix socket, mode selection |
| `cmd/luks-csi` | Entrypoint (`--mode controller\|node\|all`, `--endpoint`) |
| `deploy/helm/` | Hardened Helm chart |

## Security model

- **Keys never on disk.** Key material is held in mlock'd buffers and passed to `cryptsetup` via an OS pipe (`/dev/fd/3`). Key paths and values are never logged.
- **LUKS2 + argon2id only.** LUKS1 is rejected at format time.
- **Per-tenant Vault isolation.** Each tenant has its own AppRole and KV path (`tenants/<institution>/...`). The node service has no standing Vault access; credentials are injected per-stage via Kubernetes secrets.
- **TLS required.** The driver refuses to start if `VAULT_ADDR` is not `https://`.
- **Scoped RBAC.** The controller ClusterRole covers only what `external-provisioner` and `external-attacher` need. The only pod-create permission is a namespaced `Role` in `luks-csi-staging`, not a ClusterRole.
- **Digest-pinned images.** The Helm chart requires an explicit `image.digest`; floating tags are not accepted.

## Configuration reference

The driver binary accepts two flags:

| Flag | Default | Description |
|---|---|---|
| `--endpoint` | `unix:///csi/csi.sock` | CSI gRPC endpoint |
| `--mode` | `all` | Which services to register: `controller`, `node`, or `all` |

Runtime configuration is via environment variables:

| Variable | Description |
|---|---|
| `VAULT_ADDR` | Vault address (must be `https://`) |
| `VAULT_CACERT` | Path to the Vault CA bundle PEM |
| `LOG_LEVEL` | Log level: `DEBUG`, `INFO`, `WARN`, `ERROR` (default `INFO`) |

## End-to-end testing

The `test/e2e/` directory contains a harness for testing against a real Lima k3s cluster with Rook-Ceph and a TLS-enabled Vault. See [`test/e2e/README.md`](test/e2e/README.md) for setup instructions.

## License

Apache 2.0
