# Build-Time Debug Logging

**Date:** 2026-06-09
**Status:** Approved

## Problem

The CSI driver currently has no structured visibility into its internal operations during
development and testing. Production log levels must not expose sensitive identifiers
(institution names, Vault paths, PVC names, device paths), but developers need full
detail to confirm features work and debug failures. A runtime flag cannot satisfy both
constraints — an operator mistake or misconfiguration could enable sensitive logging in
production.

## Decision

Add a compile-time debug level gated by the `debug` build tag. When built without the
tag, the debug blocks are dead code: the compiler removes them entirely, including
argument evaluation. No runtime configuration can enable them.

## Package: `internal/dbg`

Two files, one build tag, no logic:

```
internal/dbg/
  off.go   //go:build !debug   → const Enabled = false
  on.go    //go:build debug    → const Enabled = true
```

No init functions, no slog handler changes, no imports in either file. Callers use
`slog.Debug` directly inside the guard — no new logging function to learn or import
beyond the `dbg` constant.

**Call-site pattern:**

```go
if dbg.Enabled {
    slog.Debug("create backing pvc", "ns", b.namespace, "name", name, "sc", storageClass)
}
```

When `Enabled = false`, `if false { ... }` is eliminated by the compiler. The debug
strings do not appear in the production binary.

## Log Points

### `internal/controller/controller.go` — CreateVolume

**Production (INFO):** operation start and outcome only, no identifiers.
```
"CreateVolume called"
"CreateVolume ok"
"CreateVolume failed" + gRPC code
```

**Debug adds:**
```
institution, backingStorageClass, deletionPolicy, derived backing name, full volumeID on success
```

### `internal/app/backing.go` — CreateBackingPVC / waitForBound

**Production:** `"backing: create failed"` with no PVC name or namespace.

**Debug adds:**
```
namespace, PVC name, storageClass
k8s API error detail on Create failure
PV name and bind duration on success
waitForBound poll ticks
```

### `internal/vault/client.go` — EnsureKey / DeleteKey

**Production:** `"vault: ensure key failed"` / `"vault: delete key failed"` with no path.

**Debug adds:**
```
full Vault path: secret/data/tenants/<institution>/<volume>
whether key was created vs already existed
HTTP status code on failure
```

### `internal/node/node.go` — NodeStageVolume

**Production:** `"node: stage failed"` with no device or path identifiers.

**Debug adds:**
```
device path, mapper name, staging target path
format-vs-open decision (new volume vs existing)
LUKS2 UUID after format
cryptsetup exit code on failure
```

## What Is Never Logged at Any Level

| Data | Reason |
|---|---|
| LUKS key bytes | Key material — must not appear in any log sink |
| Vault token | Credential — grants access to all tenant secrets |
| AppRole secretId | Credential — used to obtain Vault token |
| Raw Vault HTTP responses | May contain key material or token in body |

## Build Integration

**CLI / local:**
```shell
go build -tags debug ./cmd/luks-csi   # debug binary
go test  -tags debug ./...             # debug tests
```

**Dockerfile** — new optional build arg, defaults off:
```dockerfile
ARG DEBUG_BUILD=false
RUN if [ "$DEBUG_BUILD" = "true" ]; then \
      go build -tags debug -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/luks-csi ./cmd/luks-csi; \
    else \
      go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/luks-csi ./cmd/luks-csi; \
    fi
```

Build the debug node image:
```shell
podman build --target node \
  --build-arg DEBUG_BUILD=true \
  -t ghcr.io/nfcs/luks-csi:dev-debug .
```

## Existing Production Error Logs

The `slog.Error` added to `controller.go` during debugging must be replaced with
sanitised equivalents before this work is merged. The raw error and volume name it
currently logs violate the production-level rule above.

## Acceptance Criteria

1. `go build ./...` produces a binary containing no debug strings (verified with
   `strings ./luks-csi | grep -c "dbg"`).
2. `go build -tags debug ./...` produces a binary where debug log lines fire at the
   four instrumented call sites.
3. `go test -tags debug ./...` passes — no test relies on the absence of debug output.
4. `slog.Error` / `slog.Warn` / `slog.Info` calls in the four instrumented files contain
   no institution names, PVC names, Vault paths, or device paths.
5. LUKS key bytes, Vault tokens, and secretIds do not appear in any log call at any
   build variant.
