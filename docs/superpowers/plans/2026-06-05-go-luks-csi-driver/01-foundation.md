# LUKS CSI Driver — Phase 1: Foundation (Tasks 0–3)

> Part of the [Go LUKS CSI Driver plan](00-index.md). Execute phases in order; this one has no internal dependencies. REQUIRED SUB-SKILL: superpowers:subagent-driven-development or superpowers:executing-plans.

**Covers:** module bootstrap, `keyzero` (mlock'd key buffer), `csierr` (error sanitisation), `validate` (input validators).

---

## Task 0: Module bootstrap

**Files:** Create `go.mod`, `.gitignore`.

- [ ] **Step 1: Initialise the module**

```bash
cd /Users/hd25646/Documents/NFCS-secure-k8s/k8s-luks-csi-go
go mod init github.com/nfcs/k8s-luks-csi-go
go mod edit -go=1.23
```

- [ ] **Step 2: Add core dependencies**

```bash
go get github.com/container-storage-interface/spec@v1.10.0
go get google.golang.org/grpc@v1.73.0
go get github.com/hashicorp/vault/api@latest
go get github.com/hashicorp/vault/api/auth/approle@latest
go get k8s.io/client-go@v0.31.0 k8s.io/api@v0.31.0 k8s.io/apimachinery@v0.31.0
go get golang.org/x/sys/unix@latest
```
Expected: deps resolve into `go.mod`/`go.sum`, no errors.

- [ ] **Step 3: Create `.gitignore`**

```gitignore
/bin/
*.test
*.out
coverage.*
.DS_Store
```

- [ ] **Step 4: Verify the module is consistent**

Run: `go mod tidy && go vet ./... 2>&1 | head` — Expected: exit 0 (nothing to build yet).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum .gitignore
git commit -m "chore: bootstrap Go module and core dependencies"
```

---

## Task 1: `keyzero` — mlock'd, zeroed key buffer

Addresses **H-4 / M-4** (key not zeroed). Locks pages into RAM so key material is never swapped; zeroes on demand.

**Files:** Create `internal/keyzero/buffer.go`, `internal/keyzero/buffer_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/keyzero/buffer_test.go`)

```go
package keyzero

import (
	"bytes"
	"testing"
)

func TestNewBufferLength(t *testing.T) {
	b, err := NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer b.Zero()
	if got := len(b.Bytes()); got != 32 {
		t.Fatalf("len = %d, want 32", got)
	}
}

func TestZeroWipesContents(t *testing.T) {
	b, err := NewBuffer(8)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	copy(b.Bytes(), []byte("secret!!"))
	b.Zero()
	if !bytes.Equal(b.Bytes(), make([]byte, 8)) {
		t.Fatalf("buffer not zeroed: %x", b.Bytes())
	}
}

func TestZeroIsIdempotent(t *testing.T) {
	b, err := NewBuffer(4)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	b.Zero()
	b.Zero() // must not panic or double-munlock
}

func TestNewBufferRejectsNonPositive(t *testing.T) {
	if _, err := NewBuffer(0); err == nil {
		t.Fatal("expected error for zero-length buffer")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/keyzero/ -run TestNewBufferLength -v` — Expected: FAIL, `undefined: NewBuffer`.

- [ ] **Step 3: Write minimal implementation** (`internal/keyzero/buffer.go`)

```go
// Package keyzero provides mlock'd byte buffers for key material that are
// explicitly zeroed (and unlocked) when no longer needed.
package keyzero

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Buffer holds key material in memory locked against swap.
type Buffer struct {
	data   []byte
	zeroed bool
}

// NewBuffer allocates an mlock'd buffer of n bytes. The caller MUST call
// Zero (typically via defer) to wipe and unlock the pages.
func NewBuffer(n int) (*Buffer, error) {
	if n <= 0 {
		return nil, fmt.Errorf("keyzero: buffer length must be positive, got %d", n)
	}
	data := make([]byte, n)
	if err := unix.Mlock(data); err != nil {
		return nil, fmt.Errorf("keyzero: mlock failed: %w", err)
	}
	return &Buffer{data: data}, nil
}

// Bytes returns the underlying slice. Do not retain references past Zero.
func (b *Buffer) Bytes() []byte { return b.data }

// Zero wipes the buffer and unlocks its pages. Safe to call multiple times.
func (b *Buffer) Zero() {
	if b.zeroed {
		return
	}
	for i := range b.data {
		b.data[i] = 0
	}
	_ = unix.Munlock(b.data)
	b.zeroed = true
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/keyzero/ -v` — Expected: PASS (4 tests).

> **Executor note:** `mlock` can fail with `EPERM`/`ENOMEM` under a low `RLIMIT_MEMLOCK`. Small buffers usually succeed on dev machines. The node container manifest (Task 16) requests the `IPC_LOCK` capability. Do **not** add a non-mlock fallback — silently skipping mlock reintroduces the swap-leak finding.

- [ ] **Step 5: Commit**

```bash
git add internal/keyzero/ go.mod go.sum
git commit -m "feat(keyzero): add mlock'd, zeroed key buffer"
```

---

## Task 2: `csierr` — error-sanitisation boundary

Addresses **H-2** (raw errors leaking to callers). Full detail stays loggable via `errors.Is/Unwrap`; the gRPC status carries only a code + generic message.

**Files:** Create `internal/csierr/csierr.go`, `internal/csierr/csierr_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/csierr/csierr_test.go`)

```go
package csierr

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWrapHidesDetailButKeepsCode(t *testing.T) {
	internal := errors.New("vault 403 for token hvs.CAESIJ-secret-fragment")
	err := Wrap(codes.PermissionDenied, "fetch key", internal)

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Wrap did not return a gRPC status error")
	}
	if st.Code() != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", st.Code())
	}
	if strings.Contains(st.Message(), "hvs.CAESIJ") ||
		strings.Contains(st.Message(), "secret-fragment") {
		t.Fatalf("status message leaked secret: %q", st.Message())
	}
}

func TestWrapNilReturnsNil(t *testing.T) {
	if err := Wrap(codes.Internal, "op", nil); err != nil {
		t.Fatalf("Wrap(nil) = %v, want nil", err)
	}
}

func TestUnwrapExposesOriginalInternally(t *testing.T) {
	internal := errors.New("boom")
	err := Wrap(codes.Internal, "op", internal)
	if !errors.Is(err, internal) {
		t.Fatal("errors.Is could not recover the wrapped internal error")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/csierr/ -run TestWrapHidesDetailButKeepsCode -v` — Expected: FAIL, `undefined: Wrap`.

- [ ] **Step 3: Write minimal implementation** (`internal/csierr/csierr.go`)

```go
// Package csierr converts rich internal errors into sanitised gRPC statuses.
// Internal detail is preserved for logging via errors.Is/Unwrap but never
// placed in the status message returned to a caller.
package csierr

import (
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type sanitised struct {
	st      *status.Status
	wrapped error
}

func (e *sanitised) Error() string             { return e.st.Err().Error() }
func (e *sanitised) Unwrap() error             { return e.wrapped }
func (e *sanitised) GRPCStatus() *status.Status { return e.st }

// Wrap converts an internal error into a gRPC status carrying only `code`
// and a generic "<op> failed" message. Returns nil if err is nil.
func Wrap(code codes.Code, op string, err error) error {
	if err == nil {
		return nil
	}
	return &sanitised{
		st:      status.New(code, fmt.Sprintf("%s failed", op)),
		wrapped: err,
	}
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/csierr/ -v` — Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/csierr/
git commit -m "feat(csierr): add error-sanitisation gRPC boundary"
```

---

## Task 3: `validate` — shared input validators

Addresses **M-5** (unvalidated Vault-path inputs) and underpins **H-5** (mapper-name injection). Regexes live in exactly one place.

**Files:** Create `internal/validate/validate.go`, `internal/validate/validate_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/validate/validate_test.go`)

```go
package validate

import (
	"strings"
	"testing"
)

func TestInstitution(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"simple", "bristol", false},
		{"dots-dashes", "uni-bristol.ac.uk", false},
		{"alnum", "inst01", false},
		{"empty", "", true},
		{"leading-dot", ".bristol", true},
		{"traversal", "../secret/admin", true},
		{"slash", "a/b", true},
		{"too-long", strings.Repeat("a", 64), true},
		{"newline", "bristol\nx", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Institution(tt.in); (err != nil) != tt.wantErr {
				t.Fatalf("Institution(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestVolumeName(t *testing.T) {
	if err := VolumeName("pvc-abc123"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	if err := VolumeName("../escape"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestMapperName(t *testing.T) {
	if err := MapperName("luks-0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("valid mapper name rejected: %v", err)
	}
	for _, b := range []string{
		"luks-XYZ",
		"luks-0123456789abcdef0123456789abcde",   // 31 hex
		"luks-0123456789abcdef0123456789abcdef0", // 33 hex
		"evil; rm -rf /",
		"0123456789abcdef0123456789abcdef", // no prefix
	} {
		if err := MapperName(b); err == nil {
			t.Fatalf("invalid mapper name accepted: %q", b)
		}
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/validate/ -run TestInstitution -v` — Expected: FAIL, `undefined: Institution`.

- [ ] **Step 3: Write minimal implementation** (`internal/validate/validate.go`)

```go
// Package validate centralises validation for values that flow into Vault
// paths, device-mapper names, and cryptsetup arguments.
package validate

import (
	"fmt"
	"regexp"
)

// nameRE: start alphanumeric, then up to 62 of [alnum . _ -]; total 1..63.
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

// mapperRE: a SHA-256-derived mapper name "luks-" + 32 hex chars.
var mapperRE = regexp.MustCompile(`^luks-[0-9a-f]{32}$`)

// Institution validates a tenant identifier used in Vault paths.
func Institution(s string) error {
	if !nameRE.MatchString(s) {
		return fmt.Errorf("validate: invalid institution %q", s)
	}
	return nil
}

// VolumeName validates a CSI volume name used in Vault paths.
func VolumeName(s string) error {
	if !nameRE.MatchString(s) {
		return fmt.Errorf("validate: invalid volume name %q", s)
	}
	return nil
}

// MapperName validates a device-mapper target name before use in a
// /dev/mapper/ path or as a cryptsetup argument.
func MapperName(s string) error {
	if !mapperRE.MatchString(s) {
		return fmt.Errorf("validate: invalid mapper name %q", s)
	}
	return nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/validate/ -v` — Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/validate/
git commit -m "feat(validate): add institution/volume/mapper-name validators"
```

---

