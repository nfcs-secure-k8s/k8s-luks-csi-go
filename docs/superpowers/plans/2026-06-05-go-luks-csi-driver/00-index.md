# Go LUKS CSI Driver Implementation Plan — Index

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **This is the index.** The plan is split into four phase files (see [Phase files](#phase-files-execute-in-order) below); execute them in order. This `/superpowers:writing-plans` artifact lives under `k8s-luks-csi-go/docs/superpowers/plans/2026-06-05-go-luks-csi-driver/`.

## Context

The existing LUKS CSI driver lives in `k8s-block-storage-luks-csi-config` and is written in Python. A consolidated security review found 3 Critical, 17 High, and 12 Medium findings, several of them **architectural** (plaintext Vault transport, a shared Vault role with no tenant isolation, a staging-pod mechanism that forces cluster-wide pod-create on the node). The approved design spec (`k8s-luks-csi-go/docs/superpowers/specs/2026-06-05-go-luks-csi-driver-design.md`) decides to **rewrite the driver in Go** — the native CSI language — and fix every review finding *by design* rather than retrofitting the Python codebase.

This plan turns that design into a bite-sized, test-first implementation sequence. The outcome is a single Go binary (`luks.csi.nfcs.io`) exposing CSI Identity/Controller/Node services, with per-tenant Vault key isolation, mlock'd/zeroed key buffers, fd-based key passing to `cryptsetup`, a VolumeAttachment-based device path (no cluster-wide node pod-create), a hardened Helm chart, and a supply-chain-hardened CI pipeline.

**Build order is bottom-up:** leaf packages with no internal dependencies first (`keyzero`, `csierr`, `validate`), then the domain wrappers (`luks`, `vault`, `device`), then the thin CSI services that compose them (`identity`, `controller`, `node`), then the server/entrypoint, then deploy + CI + test harnesses. Every task is TDD: failing test → run-it-fails → minimal impl → run-it-passes → commit.

**Tech stack:** Go 1.23+, `github.com/container-storage-interface/spec` v1.10.0, `google.golang.org/grpc` v1.73.0, `github.com/hashicorp/vault/api` (+ `/api/auth/approle`), `k8s.io/client-go` v0.31, `log/slog`. Tooling: `golangci-lint`, `gosec`, `govulncheck`, `gitleaks`, `cosign`. Tests: `go test -race`, `kubernetes-csi/csi-test` sanity suite, `controller-runtime/envtest`, Lima k3s + Rook-Ceph e2e with a **TLS Vault**.

> Library-API details below were confirmed against current docs via context7: Vault Go AppRole login + KV v2 (`client.KVv2(mount).Get/Put`) + token-renewal goroutine pattern; client-go `rest.InClusterConfig()` (auto-refreshes the projected SA token via `BearerTokenFile`); gRPC-Go `net.Listen("unix", path)` + `grpc.NewServer()` + service registration; CSI spec Go bindings `RegisterIdentityServer/RegisterControllerServer/RegisterNodeServer`.

---

## File Structure

| Path | Responsibility | Depends on |
|---|---|---|
| `go.mod`, `go.sum` | Module `github.com/nfcs/k8s-luks-csi-go`, Go 1.23 | — |
| `internal/keyzero/buffer.go` | `mlock`-ed, defer-zeroed key buffer | `golang.org/x/sys/unix` |
| `internal/csierr/csierr.go` | Internal-error → sanitised gRPC status boundary | `grpc/codes`, `grpc/status` |
| `internal/validate/validate.go` | Shared validators (institution, volume, mapper name) | — |
| `internal/luks/runner.go` | `Runner` interface + real fd-based `exec.Cmd` impl | `keyzero` |
| `internal/luks/luks.go` | Idempotent `cryptsetup`/`mkfs` wrappers | `keyzero`, `validate`, runner |
| `internal/vault/client.go` | TLS-required, per-tenant scoped Vault client + renewal | `keyzero`, `validate` |
| `internal/vault/rotate.go` | Version-walk rotation logic | `vault/client` |
| `internal/device/resolver.go` | `Resolver` registry (Local/Longhorn/CephRBD/by-id) | `validate` |
| `internal/device/attach.go` | `Attach`/`Detach` via VolumeAttachment + scoped staging | resolver, client-go |
| `internal/identity/identity.go` | CSI Identity service | csi spec |
| `internal/controller/controller.go` | CSI Controller service | vault, device, csierr, validate, client-go |
| `internal/node/node.go` | CSI Node service | luks, vault, device, keyzero, csierr |
| `internal/server/server.go` | gRPC bootstrap, Unix socket, mode wiring | all services |
| `cmd/luks-csi/main.go` | Entrypoint, `--mode` flag, slog setup | server |
| `deploy/helm/` | Hardened chart | — |
| `.github/workflows/ci.yml` | build/sign/scan, SHA-pinned | — |
| `test/sanity/sanity_test.go` | csi-test sanity suite | server |
| `test/e2e/` | Lima k3s + Rook-Ceph + TLS Vault harness | — |

**Security-finding → task index** (full mapping in spec §5): keyzero=H-4/M-4 (Task 1); csierr=H-2 (Task 2); validate=M-5/H-5 (Task 3); luks fd-keys=C-2, KDF=M-2, mapper=H-5 (Tasks 4–5); vault TLS=C-1, tenant scope=H-1, token lifecycle=H-6, rotation=H-12 (Tasks 6–7); device attach=H-3, bind-mount=M-3 (Tasks 8–9); controller naming=M-1, Retain default=M-6, RBAC=H-11 (Task 11); node logging=H-7 (Task 12); deploy securityContext=C-4/H-8/H-9/H-10, NetworkPolicy=M-8, podInfoOnMount=M-7, image=H-16/M-11/M-12 (Tasks 16–17); CI=C-3/H-13/H-14/H-15/M-9/M-10 (Task 18).

---


## Phase files (execute in order)



This plan is split into four sequential phases. Each phase builds on the previous one (strict bottom-up dependency chain), so run them in order. Within a phase, execute tasks top-to-bottom.



| Phase | File | Tasks | Scope |

|---|---|---|---|

| 1 | [01-foundation.md](01-foundation.md) | 0–3 | module bootstrap, `keyzero`, `csierr`, `validate` |

| 2 | [02-domain.md](02-domain.md) | 4–9 | `luks`, `vault`, `device` |

| 3 | [03-services.md](03-services.md) | 10–15 | `identity`, `controller`, `node`, `server`, `cmd`, wiring |

| 4 | [04-deploy-ci-test.md](04-deploy-ci-test.md) | 16–19 | Helm chart, image, CI, sanity + e2e |


> Each task is TDD: failing test → run-it-fails → minimal impl → run-it-passes → commit. Complete code is given in every step.

---

## Final verification

Run the full gate locally before opening a PR:

- [ ] `go build ./...` — compiles clean.
- [ ] `go test ./... -race` — all unit + sanity tests pass with the race detector.
- [ ] `go vet ./...` and `golangci-lint run` — no findings.
- [ ] `govulncheck ./...` — no known vulnerable deps.
- [ ] `gitleaks detect --no-banner` — no secrets in the tree (**C-3**).
- [ ] `helm lint deploy/helm/` + `helm template luks-csi deploy/helm/` — rendered manifests contain, for every workload container: `runAsNonRoot`/`privileged` as designed, `seccompProfile`, resource `limits`; plus the `luks-csi-staging` namespaced `Role` (not a ClusterRole), `CSIDriver.podInfoOnMount: true`, and the egress NetworkPolicies.
- [ ] `docker build --target controller .` and `--target node .` — both images build; controller image runs as UID 65532.
- [ ] **e2e (gated):** run the `test/e2e` harness against Lima k3s + Rook-Ceph + TLS Vault; confirm a pod mounts a LUKS-backed PVC, writes/reads data, and that the node SA has **no** standing Vault access (attempt a Vault read with the node SA token → denied).

**Security-finding coverage self-check:** every row in spec §5 maps to a task above (see the finding→task index in the File Structure section). Confirm each Critical/High/Medium is exercised by either a unit test, a rendered-manifest assertion, or the e2e gate before declaring done.

