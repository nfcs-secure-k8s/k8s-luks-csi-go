# Design — Go LUKS CSI Driver

- **Date:** 2026-06-05
- **Status:** Approved (design phase) — implementation plan to follow
- **Repo:** `k8s-luks-csi-go` (greenfield)
- **Supersedes:** Python CSI driver in `k8s-block-storage-luks-csi-config`

---

## 1. Context & motivation

The existing LUKS CSI driver (`k8s-block-storage-luks-csi-config`) is a Python
implementation that provisions LUKS-encrypted block volumes layered over an
arbitrary backing StorageClass, with keys stored in HashiCorp Vault. A
consolidated security review (`SECURITY-REVIEW-CONSOLIDATED.md` in that repo)
identified 3 Critical, 17 High, and 12 Medium findings spanning key handling,
transport security, tenant isolation, RBAC, and supply chain.

Several findings are *architectural* (plaintext Vault transport, a shared Vault
role with no tenant isolation, a staging-pod mechanism that forces cluster-wide
pod-create on the node). Rather than retrofit fixes onto the Python codebase, we
are rewriting the driver in **Go** — the native language of the CSI ecosystem —
and addressing the review findings **by design**.

---

## 2. Goals, non-goals & scope

**Goals**

- A from-scratch Go CSI driver: Identity + Controller + Node services in one binary.
- Layered model preserved: encrypt on top of an arbitrary backing StorageClass.
- Keys in Vault, with **per-tenant isolation enforced by design**.
- Every Critical/High/Medium finding from the review addressed by a concrete design decision (§5).
- Three-tier test strategy that covers the logic bugs the Python harness cannot reach.

**Non-goals (this project)**

- The kopf operator (`k8s-block-storage-luks-operator-config`) — out of scope.
- The docs site (`luks-k8s-automation`) — out of scope.
- Reading or migrating volumes/keys created by the Python driver.

**Locked scope decisions**

| Decision | Choice |
|---|---|
| What to port | CSI driver only (controller + node) |
| Architecture fidelity | Pragmatic redesign — preserve external contract, fix structural rough edges |
| Migration | Greenfield — own driver name `luks.csi.nfcs.io`, own volume-ID format and Vault path scheme |
| Key delivery | Option 1 (per-namespace Vault credential via CSI node-stage secrets); Options 2 & 3 documented for future revisit (Appendix A) |

---

## 3. Architecture

Standard CSI three-service split, a single binary, mode selected by `--mode` flag
(`controller` | `node` | `all`):

- **Identity / Controller / Node** gRPC services over a Unix socket, using the
  official `github.com/container-storage-interface/spec` Go bindings and
  `google.golang.org/grpc`.
- Pairs with the stock Kubernetes-CSI sidecars: `external-provisioner`,
  **`external-attacher`** (new vs. the Python design), and `node-driver-registrar`.
- `k8s.io/client-go` for the Kubernetes API; `github.com/hashicorp/vault/api` for Vault.

### 3.1 Device attachment — staging-pod redesign

The Python driver creates a throwaway **staging pod** to make the backing block
device appear on a node — a mechanism that forces cluster-wide pod create/delete
on the node DaemonSet SA (finding H-3).

**Redesign:**

- The controller provisions the backing PVC and drives device attachment via a
  proper `VolumeAttachment` + `external-attacher` / `ControllerPublishVolume`.
- For **attach-based backends** (cloud block: EBS, GCE PD, Cinder, Azure Disk)
  the device appears on the node with **no staging pod and no node pod-create
  permission at all**.
- For **map-on-stage backends** (Ceph RBD, Longhorn) the backing device only
  materialises when the backing driver's `NodeStageVolume` runs, so a minimal
  staging mechanism is still required. Its RBAC is scoped to a **single dedicated
  `luks-csi-staging` namespace via a namespaced `Role`** — never cluster-wide.
  Blast radius shrinks from "create any pod in any namespace" to "create pause
  pods in one namespace."

---

## 4. Key delivery & tenant isolation (Option 1)

1. **Bootstrap:** each tenant namespace holds a small Secret containing a
   **Vault role reference** scoped to that institution — not the LUKS key.
2. The StorageClass sets `csi.storage.k8s.io/node-stage-secret-name`/`-namespace`
   with `${pvc.namespace}` templating.
3. Kubelet reads the Secret from the **workload's own namespace** and passes it
   into `NodeStageVolumeRequest.secrets`.
4. The node authenticates to Vault **scoped to that institution only**, fetches
   the key for the specific volume, uses it, then zeroes it. Never cached across
   volumes.
5. Vault policy templating restricts each role to
   `secret/data/tenants/<institution>/*`.

**Isolation properties:**

- The node DaemonSet SA has **zero standing Vault access**.
- Cross-tenant access requires compromising a tenant's namespace RBAC *and* the
  Vault policy restricts regardless.
- The bootstrap Secret is low-sensitivity (a role pointer, not key material) and
  namespace-bounded.

Future upgrade paths are documented in Appendix A.

---

## 5. Security-by-design mapping

| Finding | Design decision |
|---|---|
| **C-1** Vault plaintext HTTP | Vault client requires `https://` + CA bundle. Process refuses to start if scheme is not `https`. No insecure-skip path. |
| **C-2** key written to temp file | Keys handed to `cryptsetup` via `--key-file /dev/fd/N` (`exec.Cmd` pipe + `ExtraFiles`). Never touches filesystem. |
| **C-3** committed passphrase | No secrets in repo. e2e mints ephemeral keys at runtime. `gitleaks` in CI. |
| **H-1** no tenant isolation | Per-namespace scoped Vault auth via node-stage secrets (§4). Node SA has no standing Vault access. |
| **H-2** raw errors to gRPC | `csierr` boundary: full detail logged internally; callers receive `codes.*` + generic message only. |
| **H-3** node pod-create cluster-wide | Removed via VolumeAttachment-based attach; where staging is unavoidable, scoped to `luks-csi-staging` namespace via `Role` (§3.1). |
| **H-4 / M-4** key not zeroed | `keyzero` package: `mlock`-ed byte buffers, explicit zero on `defer`. |
| **H-5** mapper-name injection | Mapper names are SHA-256-derived hex, regex-validated (`^luks-[0-9a-f]{32}$`) before use in `/dev/mapper/`. |
| **H-6** Vault token lifecycle | Cached Vault client with lease-aware renewal; `client-go` in-cluster config auto-refreshes projected SA token. `Forbidden` errors handled distinctly — no credential fragments to callers. |
| **H-7** DEBUG logging hardcoded | Structured `log/slog` JSON; level via `LOG_LEVEL` env, default INFO. Key material and key-file paths never logged. |
| **C-4 / H-8** controller no securityContext | `runAsNonRoot: true`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`. |
| **H-9** no seccomp | `RuntimeDefault` on every workload; scoped `Localhost` profile considered for node DaemonSet (open question §11). |
| **H-10** no resource limits | Requests and limits on every container. |
| **H-11** controller cluster-wide PVC delete | PVC permissions narrowed to what `external-provisioner` actually requires; label selectors where supported. |
| **H-12** rotation bricks volume | Rotation walks Vault versions backward until one authenticates; per-volume lock serialises concurrent stage calls. |
| **H-13 / H-14 / H-15** CI supply chain | Actions pinned to full SHAs; `cosign` signing + SBOM + provenance; `govulncheck` gate on deps. |
| **H-16 / M-11 / M-12** image hardening | Multi-stage build → distroless runtime, non-root `USER`, digest-pinned base. (Node container needs root for `cryptsetup`/mount — controller does not.) |
| **M-1** PVC-name collision | Backing names = `luks-backing-` + SHA-256 suffix. Collision-resistant. |
| **M-2** weak KDF | `luks1` rejected outright. `luksFormat` always passes `--pbkdf argon2id`. dm-integrity (`--integrity`) offered as StorageClass opt-in. |
| **M-3** full `/dev` mount | Device-specific bind mounts for the resolved block device only — no blanket host `/dev`. |
| **M-5** unvalidated path inputs | `institution` and volume names validated against `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$` before use in Vault paths. |
| **M-6** destructive delete default | `reclaimPolicy`/`deletionPolicy` default to `Retain`. `Delete` is opt-in via a separate StorageClass. |
| **M-7** podInfoOnMount | `CSIDriver.podInfoOnMount: true` for mount audit identity. |
| **M-8** no NetworkPolicy | Chart ships NetworkPolicies restricting egress to Vault (TLS), API server (443), and DNS (53). |
| **M-9 / M-10** CI token handling | `GITHUB_TOKEN` via `env:`; top-level least-privilege `permissions:` block. |
| **L-1** dead code | N/A — greenfield; no vestigial Secret-based code paths. |

---

## 6. Repo layout

```
k8s-luks-csi-go/
├── cmd/luks-csi/             # entrypoint, --mode flag, gRPC bootstrap
├── internal/
│   ├── identity/             # CSI Identity service
│   ├── controller/           # CSI Controller service
│   ├── node/                 # CSI Node service
│   ├── luks/                 # cryptsetup wrapper (fd-based keys, idempotent)
│   ├── vault/                # TLS-required, per-tenant scoped client
│   ├── device/               # backend resolver registry + attach logic
│   ├── keyzero/              # mlock'd, zeroed key buffers
│   └── csierr/               # error-sanitisation boundary
├── deploy/helm/              # hardened chart (securityContext, seccomp,
│                             #   limits, scoped RBAC, NetworkPolicies)
├── test/
│   ├── e2e/                  # Lima k3s + Rook-Ceph harness (TLS Vault)
│   └── sanity/               # kubernetes-csi/csi-test sanity suite
├── .github/workflows/        # build / sign / scan, SHA-pinned
└── docs/
    └── superpowers/specs/    # this document
```

Go 1.23+. Lint/scan gate: `golangci-lint`, `gosec`, `govulncheck`.

---

## 7. Component responsibilities

| Package | Responsibility | Key interface |
|---|---|---|
| `luks` | Idempotent `cryptsetup`/`mkfs` wrappers. Keys accepted as `keyzero.Buffer`. Exec behind `Runner` interface for unit-test injection. | `Format`, `Open`, `Close`, `MakeFS`, `AddKey`, `RemoveKey`, `IsLUKS` |
| `vault` | Authenticated TLS Vault client, per-tenant scoped from a role reference. Owns token renewal. Validates institution/volume names. | `ReadKey`, `EnsureKey`, `CurrentVersion`, `DeleteKey`, `Rotate` |
| `device` | `Resolver` registry (Local, Longhorn, CephRBD, generic by-id) + `Attach`/`Detach` via VolumeAttachment + scoped staging fallback. | `Attach`, `Detach`, `Resolve`, `WaitForDevice` |
| `keyzero` | `mlock`-ed byte buffer, `Zero()`, defer-safe API. | `NewBuffer(n)`, `Buffer.Bytes()`, `Buffer.Zero()` |
| `csierr` | Maps internal errors to sanitised gRPC statuses. | `Wrap(err) error` |
| `controller` / `node` / `identity` | CSI service implementations composing the above; thin, logic pushed into testable packages. | CSI gRPC implementations |

---

## 8. Key volume lifecycle flows

**CreateVolume:** validate params → derive collision-resistant backing PVC name
(SHA-256 suffix) → create backing PVC → wait Bound → ensure Vault key for
`(institution, volume)` → return `volume_context` (backing PV ref, luksType,
filesystem, institution, vault path, deletion policy). Default policy: `Retain`.

**ControllerPublishVolume:** create/confirm `VolumeAttachment` for the backing PV
on the target node (attach-based backends). No-op for map-on-stage backends
(handled at stage time).

**NodeStageVolume:** read per-tenant Vault credential from `request.secrets` →
resolve + attach device → fetch key into `keyzero.Buffer` → `isLUKS`? format
(argon2id) + open + mkfs : open-with-rotation → mount staging path → zero key in
`defer`.

**Rotation:** on wrong-key open, walk Vault versions backward until one
authenticates → `luksAddKey` new + `luksRemoveKey` old → per-volume lock
serialises concurrent calls.

**NodeUnstageVolume:** umount (lazy fallback) → `cryptsetup luksClose` → check
`dmsetup` fallback return codes (no false success) → detach.

**DeleteVolume:** if `deletionPolicy=Delete`: `vault.DeleteKey` (all versions) →
delete backing PVC. Default policy (`Retain`) is a no-op on the Vault key.

---

## 9. Testing strategy

**Unit (`go test`, table-driven, host-runnable)**
Covers precisely the logic bugs the Python e2e harness could not reach:
`luks` wrapper against a fake `Runner`; mapper-name validation; backing-PVC-name
collision resistance; **rotation version-walk**; KDF enforcement (`luks1`
rejected); `csierr` sanitisation; `keyzero` zeroing; Vault path injection
validation.

**CSI sanity**
The official `kubernetes-csi/csi-test` sanity suite run against the driver Unix
socket. Validates spec compliance without a real cluster.

**Integration / e2e**
- `envtest` + fake clientset for controller logic (no cluster needed).
- The existing Lima k3s + Rook-Ceph e2e harness adapted as the integration gate,
  extended with a **TLS-enabled Vault** so C-1 is genuinely exercised (the Python
  harness only ran dev-mode HTTP Vault).

---

## 10. CI/CD & supply chain

GitHub Actions, all actions SHA-pinned, least-privilege `permissions` block:

1. `go test ./... -race` + `golangci-lint` + `gosec` + `govulncheck` + `gitleaks`
2. Multi-arch build (`linux/amd64`, `linux/arm64`) → distroless, non-root
3. `cosign` sign + SBOM + SLSA provenance attestation on push to `main`/tags
4. `helm lint` + rendered-manifest policy check

---

## 11. Deferred decisions (resolved)

- **Node seccomp:** use `RuntimeDefault` in v1. Scoped `Localhost` profile deferred.
- **dm-integrity:** not in v1. `--integrity` opt-in deferred to a follow-up.
- **Volume expansion / snapshots:** not in initial scope; flagged as operational
  follow-ups.

---

## Appendix A — deferred key-delivery options

Evaluated during brainstorming; deferred in favour of Option 1. Recorded for
future revisit without re-deriving the analysis.

**Option 2 — Vault Agent sidecar with workload identity.**
Per-tenant Vault roles; a Vault Agent injector renders the key via pod annotations.
Strongest alignment with Vault's native model. Cost: injector webhook to operate,
sidecar lifecycle awkward inside a node DaemonSet, more infrastructure to manage.
Revisit if the platform already runs the Vault Agent injector for other workloads.

**Option 3 — controller-minted, response-wrapped per-volume tokens.**
At provision time the controller mints a short-lived, single-use Vault token scoped
to exactly one volume's key path and passes it **response-wrapped** in
`volume_context`. The node unwraps it to read just that one key, after which the
token expires. Tightest possible isolation: per-volume, ephemeral, zero standing
node credential. Cost: TTL coordination, wrap/unwrap lifecycle, retry/repair
semantics when a token expires before stage completes.
Revisit if per-volume (not per-tenant) isolation becomes a requirement.
