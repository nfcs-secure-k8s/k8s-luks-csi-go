# Manual Testing Guide

End-to-end test of the LUKS CSI driver using Lima k3s + a loop device + a TLS Vault.
The loop device stands in for a real block storage backend (Ceph, Longhorn, etc.) so the
full node path — luksFormat, luksOpen, mount, bind-mount, umount, luksClose — runs locally.

---

## Prerequisites

| Tool | Version | Notes |
|---|---|---|
| Lima | 0.21+ | `brew install lima` |
| kubectl | 1.27+ | `brew install kubectl` |
| helm | 3.x | `brew install helm` |
| Vault CLI | 1.12+ | `brew install vault` |
| Docker / nerdctl | any | image build + load |

---

## 1. Build the driver image

```bash
# From the repo root
docker build --target node -t luks-csi:dev-node .
docker build --target controller -t luks-csi:dev-controller .
```

For the demo we use a single tag and run `--mode all`, so just build the node target
(it includes cryptsetup) and tag it as `dev`:

```bash
docker build --target node -t luks-csi:dev .
```

---

## 2. Start Lima k3s

Create a Lima VM with k3s (Lima ships a k3s template):

```bash
limactl start template://k3s
```

Wait until the control plane is ready, then point `kubectl` at it:

```bash
export KUBECONFIG=$(limactl list k3s --format '{{.Dir}}/copied-to-host/kubeconfig.yaml')
kubectl get nodes
# NAME       STATUS   ROLES                  AGE   VERSION
# lima-k3s   Ready    control-plane,master   ...   v1.28.*
```

### Load the driver image into the Lima VM

```bash
# Save the image from Docker and load it into the Lima containerd
docker save luks-csi:dev | limactl shell k3s sudo ctr images import -
```

Verify:

```bash
limactl shell k3s sudo ctr images ls | grep luks-csi
```

---

## 3. Create a loop device inside the Lima VM

The loop device plays the role of the backing block PV. Attach to the VM shell:

```bash
limactl shell k3s
```

Inside the VM:

```bash
# Create a 2 GiB sparse image for the backing store
sudo dd if=/dev/zero of=/tmp/luks-backing.img bs=1M count=2048 status=progress

# Attach it as a loop device
LOOP=$(sudo losetup --show -f /tmp/luks-backing.img)
echo "Loop device: $LOOP"   # e.g. /dev/loop0

# Confirm it exists
lsblk $LOOP
exit
```

Note the loop device path (e.g. `/dev/loop0`). You will use it in the StorageClass below.

---

## 4. Set up a TLS Vault

### 4a. Generate a self-signed CA and server certificate

Run these on your laptop (not inside Lima):

```bash
mkdir -p /tmp/vault-tls && cd /tmp/vault-tls

# CA key + cert
openssl genrsa -out ca.key 4096
openssl req -x509 -new -nodes -key ca.key -sha256 -days 3650 \
  -subj "/CN=LUKS-CSI Test CA" -out ca.pem

# Server key + CSR
openssl genrsa -out vault.key 2048
openssl req -new -key vault.key \
  -subj "/CN=vault.luks-csi.svc" -out vault.csr

# Sign with the CA, add SAN for the k3s service DNS
cat > vault-ext.cnf <<'EOF'
[SAN]
subjectAltName=DNS:vault.luks-csi.svc,DNS:vault.luks-csi.svc.cluster.local,DNS:localhost,IP:127.0.0.1
EOF
openssl x509 -req -in vault.csr -CA ca.pem -CAkey ca.key -CAcreateserial \
  -out vault.crt -days 365 -extfile vault-ext.cnf -extensions SAN

cd -
```

### 4b. Deploy Vault in the Lima k3s cluster

```bash
# Create namespace
kubectl create namespace luks-csi

# Upload TLS cert as a Secret
kubectl -n luks-csi create secret tls vault-tls \
  --cert=/tmp/vault-tls/vault.crt \
  --key=/tmp/vault-tls/vault.key

# Upload the CA bundle so the driver can verify Vault
kubectl -n luks-csi create secret generic luks-csi-vault-ca \
  --from-file=ca.pem=/tmp/vault-tls/ca.pem
```

Deploy Vault (minimal, dev-mode disabled, TLS on):

```yaml
# vault-deploy.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: vault-config
  namespace: luks-csi
data:
  vault.hcl: |
    storage "file" { path = "/vault/data" }
    listener "tcp" {
      address     = "0.0.0.0:8200"
      tls_cert_file = "/vault/tls/tls.crt"
      tls_key_file  = "/vault/tls/tls.key"
    }
    ui = false
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vault
  namespace: luks-csi
spec:
  replicas: 1
  selector: { matchLabels: { app: vault } }
  template:
    metadata: { labels: { app: vault } }
    spec:
      containers:
      - name: vault
        image: hashicorp/vault:1.15
        args: ["vault", "server", "-config=/vault/config/vault.hcl"]
        ports: [{ containerPort: 8200 }]
        securityContext: { capabilities: { add: ["IPC_LOCK"] } }
        volumeMounts:
        - { name: config, mountPath: /vault/config }
        - { name: tls,    mountPath: /vault/tls,    readOnly: true }
        - { name: data,   mountPath: /vault/data }
      volumes:
      - { name: config, configMap: { name: vault-config } }
      - { name: tls,    secret:    { secretName: vault-tls } }
      - { name: data,   emptyDir:  {} }
---
apiVersion: v1
kind: Service
metadata:
  name: vault
  namespace: luks-csi
spec:
  selector: { app: vault }
  ports: [{ port: 8200, targetPort: 8200 }]
```

```bash
kubectl apply -f vault-deploy.yaml
kubectl -n luks-csi wait --for=condition=available deploy/vault --timeout=60s
```

### 4c. Initialise and configure Vault

Port-forward so the CLI can reach it:

```bash
kubectl -n luks-csi port-forward svc/vault 8200:8200 &
export VAULT_ADDR=https://127.0.0.1:8200
export VAULT_CACERT=/tmp/vault-tls/ca.pem
```

Initialise (single unseal key for the demo):

```bash
vault operator init -key-shares=1 -key-threshold=1 > /tmp/vault-init.txt
UNSEAL_KEY=$(grep 'Unseal Key 1:' /tmp/vault-init.txt | awk '{print $NF}')
ROOT_TOKEN=$(grep 'Initial Root Token:' /tmp/vault-init.txt | awk '{print $NF}')

vault operator unseal $UNSEAL_KEY
vault login $ROOT_TOKEN
```

Enable KV v2 and AppRole, then create a per-tenant policy and role:

```bash
vault secrets enable -path=secret kv-v2
vault auth enable approle

# Policy for tenant "bristol"
vault policy write tenant-bristol - <<'EOF'
path "secret/data/tenants/bristol/*"     { capabilities = ["read","create","update"] }
path "secret/metadata/tenants/bristol/*" { capabilities = ["list","delete"] }
EOF

vault write auth/approle/role/bristol \
  token_policies="tenant-bristol" \
  token_ttl=1h token_max_ttl=4h

ROLE_ID=$(vault read -field=role_id auth/approle/role/bristol/role-id)
SECRET_ID=$(vault write -f -field=secret_id auth/approle/role/bristol/secret-id)
echo "ROLE_ID:    $ROLE_ID"
echo "SECRET_ID:  $SECRET_ID"
```

---

## 5. Install the LUKS CSI driver

### 5a. Create the node-stage Vault credential secret

```bash
# This goes in the workload namespace (here: "default")
kubectl create secret generic vault-cred \
  --from-literal=roleId=$ROLE_ID \
  --from-literal=secretId=$SECRET_ID
```

### 5b. Install via Helm

For the loop-device test we run the driver in `--mode all` as a single Deployment
(no DaemonSet needed because both controller and node run in one pod):

```bash
helm install luks-csi deploy/helm/ \
  --namespace luks-csi \
  --set vault.addr=https://vault.luks-csi.svc:8200 \
  --set vault.cidr=10.0.0.0/8 \
  --set image.repository=luks-csi \
  --set image.digest="" \
  --set "image.tag=dev" \
  --set logLevel=DEBUG
```

> **Note:** The Helm chart uses `image.digest` for production pinning. For the dev
> image loaded via `ctr` above, override it to empty and add `image.tag=dev`.
> You may need to patch `deploy/helm/templates/` to allow a tag fallback for testing.

Check the driver pod is running:

```bash
kubectl -n luks-csi get pods
kubectl -n luks-csi logs -l app=luks-csi -f
```

---

## 6. Create a StorageClass using the loop device

Replace `/dev/loop0` with the actual loop device from step 3:

```yaml
# luks-sc.yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: luks-loop
provisioner: luks.csi.nfcs.io
parameters:
  institution: bristol
  backingStorageClass: standard   # k3s default SC (backing PVC is only a placeholder)
  deletionPolicy: Retain
  devicePath: /dev/loop0          # direct resolver — bypasses PV lookup for testing
  backend: direct
reclaimPolicy: Retain
volumeBindingMode: Immediate
```

```bash
kubectl apply -f luks-sc.yaml
```

---

## 7. Create a PVC and a test Pod

```yaml
# luks-pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: encrypted-test
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: luks-loop
  resources:
    requests:
      storage: 1Gi
```

```yaml
# luks-pod.yaml
apiVersion: v1
kind: Pod
metadata:
  name: luks-writer
spec:
  containers:
  - name: writer
    image: busybox
    command: ["sh", "-c", "echo 'LUKS volume works!' > /data/hello.txt && sleep 3600"]
    volumeMounts:
    - name: encrypted
      mountPath: /data
  volumes:
  - name: encrypted
    persistentVolumeClaim:
      claimName: encrypted-test
  nodeStageSecretRef:             # CSI node-stage secret (csi.storage.k8s.io convention)
    name: vault-cred
    namespace: default
```

> **Secret wiring:** k3s passes the `nodeStageSecretRef` from the PV spec or StorageClass
> annotations. For a quick test, annotate the PVC:
> ```bash
> kubectl annotate pvc encrypted-test \
>   csi.storage.k8s.io/node-stage-secret-name=vault-cred \
>   csi.storage.k8s.io/node-stage-secret-namespace=default
> ```

```bash
kubectl apply -f luks-pvc.yaml
kubectl apply -f luks-pod.yaml
kubectl wait --for=condition=ready pod/luks-writer --timeout=120s
```

---

## 8. Inspect the LUKS volume on the node

Attach to the Lima VM:

```bash
limactl shell k3s
```

**Confirm the LUKS mapper is open:**

```bash
sudo dmsetup ls
# luks-<32-hex-chars>  (253:0)

sudo dmsetup info luks-<mapper-name>
```

**Inspect the LUKS header on the loop device:**

```bash
sudo cryptsetup luksDump /dev/loop0
# Version: 2
# Cipher: aes
# Cipher mode: xts-plain64
# ...PBKDF: argon2id
```

**Confirm the ext4 filesystem is mounted:**

```bash
lsblk -o NAME,FSTYPE,MOUNTPOINT /dev/loop0
# loop0         crypto_LUKS
# └─luks-...    ext4    /var/lib/kubelet/plugins/.../staging/...

df -h | grep luks
```

**Verify data written by the Pod:**

```bash
# Find the staging mount path
STAGING=$(mount | grep luks | awk '{print $3}' | head -1)
sudo ls -la $STAGING
sudo cat $STAGING/hello.txt
# LUKS volume works!
```

**Confirm the raw device is ciphertext (not readable):**

```bash
sudo strings /dev/loop0 | grep -i "luks volume"
# (no output — data is encrypted)
```

---

## 9. Full lifecycle verification

Delete the Pod and PVC to trigger NodeUnstageVolume:

```bash
exit   # back to laptop
kubectl delete pod luks-writer
kubectl delete pvc encrypted-test
```

Back in the Lima VM, verify the mapper was closed:

```bash
limactl shell k3s
sudo dmsetup ls
# (luks-... no longer listed)
exit
```

---

## 10. Observe structured log output

The driver emits JSON logs. Filter for key events:

```bash
kubectl -n luks-csi logs -l app=luks-csi | jq 'select(.msg | test("staged|unstaged|published|unpublished"))'
```

Example output:
```json
{"time":"...","level":"INFO","msg":"staged volume","institution":"bristol","volume":"pvc-abc","mapper":"luks-a3f2..."}
{"time":"...","level":"INFO","msg":"unstaged volume","institution":"bristol","volume":"pvc-abc","mapper":"luks-a3f2..."}
```

Key observables:
- `institution` and `mapper` are logged.
- `roleId`, `secretId`, and key bytes are **never** present in the logs (H-7).

---

## 11. Cleanup

```bash
helm uninstall luks-csi -n luks-csi
kubectl delete namespace luks-csi
limactl shell k3s -- sudo losetup -d /dev/loop0
limactl shell k3s -- sudo rm /tmp/luks-backing.img
limactl stop k3s
```

---

## Reference: useful cryptsetup / dmsetup commands

| Goal | Command |
|---|---|
| List active mappers | `sudo dmsetup ls` |
| Show mapper status | `sudo dmsetup info luks-<name>` |
| Show LUKS header | `sudo cryptsetup luksDump /dev/loopN` |
| Check PBKDF is argon2id | `sudo cryptsetup luksDump /dev/loopN \| grep PBKDF` |
| Show device tree | `lsblk -o NAME,FSTYPE,SIZE,MOUNTPOINT /dev/loopN` |
| Manual luksOpen (test) | `sudo cryptsetup luksOpen /dev/loopN test-mapper --key-file /dev/stdin` |
| Manual close | `sudo cryptsetup luksClose test-mapper` |
