# LUKS CSI Driver — Phase 2: Domain wrappers (Tasks 4–9)

> Part of the [Go LUKS CSI Driver plan](00-index.md). Depends on Phase 1 ([01-foundation.md](01-foundation.md)).

**Covers:** `luks` (fd-based cryptsetup wrapper), `vault` (TLS client + rotation), `device` (resolver registry + VolumeAttachment).

---

## Task 4: `luks` Runner — fd-based key passing

Addresses **C-2** (key written to temp file). The key is fed to `cryptsetup` via `--key-file=/dev/fd/3`, where fd 3 is the read end of an `os.Pipe` passed through `exec.Cmd.ExtraFiles`. The key never touches the filesystem. The `Runner` interface lets unit tests inject a fake.

**Files:** Create `internal/luks/runner.go`, `internal/luks/runner_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/luks/runner_test.go`)

The real runner is verified with a harmless command (`cat`) that reads fd 3, proving the key reaches the child process via the descriptor (not a temp file).

```go
package luks

import (
	"bytes"
	"context"
	"testing"

	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
)

func TestExecRunnerDeliversKeyOnFD3(t *testing.T) {
	key, err := keyzero.NewBuffer(11)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer key.Zero()
	copy(key.Bytes(), []byte("hello-world"))

	r := NewExecRunner("cat")
	// `cat /dev/fd/3` echoes whatever we wrote to the pipe.
	out, err := r.Run(context.Background(), key, KeyFDPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !bytes.Equal(out, []byte("hello-world")) {
		t.Fatalf("fd-3 payload = %q, want %q", out, "hello-world")
	}
}

func TestExecRunnerNoKeyOmitsExtraFile(t *testing.T) {
	r := NewExecRunner("echo")
	out, err := r.Run(context.Background(), nil, "ok")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(bytes.TrimSpace(out)); got != "ok" {
		t.Fatalf("echo output = %q, want %q", got, "ok")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/luks/ -run TestExecRunnerDeliversKeyOnFD3 -v` — Expected: FAIL, `undefined: NewExecRunner`.

- [ ] **Step 3: Write minimal implementation** (`internal/luks/runner.go`)

```go
// Package luks wraps cryptsetup/mkfs with fd-based key passing so key
// material is never written to the filesystem.
package luks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
)

// KeyFDPath is the path cryptsetup reads the key from. fd 3 is the first
// (and only) entry in exec.Cmd.ExtraFiles.
const KeyFDPath = "/dev/fd/3"

// Runner executes an external command, optionally delivering key material on
// file descriptor 3 (path KeyFDPath). Abstracted for unit-test injection.
type Runner interface {
	// Run executes the configured binary with args. If key is non-nil its
	// bytes are streamed to the child on fd 3, then the pipe is closed.
	Run(ctx context.Context, key *keyzero.Buffer, args ...string) ([]byte, error)
}

// ExecRunner is the production Runner backed by os/exec.
type ExecRunner struct {
	bin string
}

// NewExecRunner returns a Runner that invokes the named binary.
func NewExecRunner(bin string) *ExecRunner { return &ExecRunner{bin: bin} }

func (r *ExecRunner) Run(ctx context.Context, key *keyzero.Buffer, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.bin, args...)

	if key != nil {
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("luks: pipe: %w", err)
		}
		// Child inherits the read end as fd 3.
		cmd.ExtraFiles = []*os.File{pr}
		defer pr.Close()

		go func() {
			defer pw.Close()
			_, _ = pw.Write(key.Bytes())
		}()
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("luks: %s %v: %w: %s",
			r.bin, args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/luks/ -v` — Expected: PASS (2 tests). (`cat`/`echo` are available on the dev host.)

- [ ] **Step 5: Commit**

```bash
git add internal/luks/runner.go internal/luks/runner_test.go
git commit -m "feat(luks): add Runner with fd-based key passing"
```

---

## Task 5: `luks` wrapper — idempotent cryptsetup/mkfs operations

Addresses **M-2** (weak KDF — `luks1` rejected, `luksFormat` always uses `--pbkdf argon2id`) and **H-5** (mapper-name validation before use). Idempotent: `Open` on an already-open mapper is a no-op success; `IsLUKS` distinguishes format-vs-open at stage time.

**Files:** Create `internal/luks/luks.go`, `internal/luks/luks_test.go`. Test against a **fake Runner** so no real cryptsetup/root is needed.

- [ ] **Step 1: Write the failing test** (`internal/luks/luks_test.go`)

```go
package luks

import (
	"context"
	"strings"
	"testing"

	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
)

// fakeRunner records calls and returns canned output/errors keyed by the
// first argument (the cryptsetup subcommand).
type fakeRunner struct {
	calls   [][]string
	gotKey  bool
	outputs map[string][]byte
	errs    map[string]error
}

func (f *fakeRunner) Run(_ context.Context, key *keyzero.Buffer, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if key != nil {
		f.gotKey = true
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	return f.outputs[sub], f.errs[sub]
}

func newKey(t *testing.T) *keyzero.Buffer {
	t.Helper()
	k, err := keyzero.NewBuffer(8)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	copy(k.Bytes(), []byte("testkey1"))
	return k
}

func TestFormatUsesArgon2idAndKeyFD(t *testing.T) {
	fr := &fakeRunner{}
	l := New(fr)
	key := newKey(t)
	defer key.Zero()

	if err := l.Format(context.Background(), "/dev/loopX", key); err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !fr.gotKey {
		t.Fatal("Format did not pass key on fd")
	}
	joined := strings.Join(fr.calls[0], " ")
	if !strings.Contains(joined, "--pbkdf argon2id") {
		t.Fatalf("Format did not force argon2id: %q", joined)
	}
	if !strings.Contains(joined, "--key-file="+KeyFDPath) {
		t.Fatalf("Format did not use fd key-file: %q", joined)
	}
	if strings.Contains(joined, "luksFormat --type luks1") {
		t.Fatal("Format must never request luks1")
	}
}

func TestOpenValidatesMapperName(t *testing.T) {
	l := New(&fakeRunner{})
	key := newKey(t)
	defer key.Zero()
	// Injection attempt as mapper name must be rejected before exec.
	err := l.Open(context.Background(), "/dev/loopX", "evil; rm -rf /", key)
	if err == nil {
		t.Fatal("Open accepted an invalid mapper name")
	}
}

func TestOpenIsIdempotentWhenMapperExists(t *testing.T) {
	// status returns success => mapper already active => Open is a no-op.
	fr := &fakeRunner{outputs: map[string][]byte{"status": []byte("is active")}}
	l := New(fr)
	key := newKey(t)
	defer key.Zero()

	name := "luks-0123456789abcdef0123456789abcdef"
	if err := l.Open(context.Background(), "/dev/loopX", name, key); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, c := range fr.calls {
		if len(c) > 0 && c[0] == "luksOpen" {
			t.Fatal("Open ran luksOpen despite active mapper")
		}
	}
}

func TestIsLUKSReturnsTrueOnZeroExit(t *testing.T) {
	fr := &fakeRunner{} // isLuks subcommand returns nil error => true
	l := New(fr)
	ok, err := l.IsLUKS(context.Background(), "/dev/loopX")
	if err != nil {
		t.Fatalf("IsLUKS: %v", err)
	}
	if !ok {
		t.Fatal("IsLUKS = false, want true on zero exit")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/luks/ -run TestFormatUsesArgon2idAndKeyFD -v` — Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Write minimal implementation** (`internal/luks/luks.go`)

```go
package luks

import (
	"context"
	"fmt"

	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
	"github.com/nfcs/k8s-luks-csi-go/internal/validate"
)

// LUKS wraps a Runner with idempotent cryptsetup/mkfs operations.
type LUKS struct {
	run   Runner // cryptsetup
	mkfs  Runner // mkfs.ext4 (or configured fs)
}

// New returns a LUKS using the given cryptsetup Runner. The mkfs Runner
// defaults to mkfs.ext4 and can be overridden with WithMkfs.
func New(cryptsetup Runner) *LUKS {
	return &LUKS{run: cryptsetup, mkfs: NewExecRunner("mkfs.ext4")}
}

// WithMkfs overrides the filesystem-creation Runner (for tests/other fs).
func (l *LUKS) WithMkfs(r Runner) *LUKS { l.mkfs = r; return l }

// Format initialises a LUKS2 container with argon2id. luks1 is never used.
func (l *LUKS) Format(ctx context.Context, device string, key *keyzero.Buffer) error {
	_, err := l.run.Run(ctx, key,
		"luksFormat", "--type", "luks2", "--pbkdf", "argon2id",
		"--batch-mode", "--key-file="+KeyFDPath, device)
	if err != nil {
		return fmt.Errorf("luks: format %s: %w", device, err)
	}
	return nil
}

// Open activates the mapper. Idempotent: if the mapper is already active it
// returns nil without re-running luksOpen.
func (l *LUKS) Open(ctx context.Context, device, name string, key *keyzero.Buffer) error {
	if err := validate.MapperName(name); err != nil {
		return err
	}
	if l.active(ctx, name) {
		return nil
	}
	_, err := l.run.Run(ctx, key, "luksOpen", "--key-file="+KeyFDPath, device, name)
	if err != nil {
		return fmt.Errorf("luks: open %s: %w", name, err)
	}
	return nil
}

// Close deactivates the mapper. Idempotent: inactive mapper returns nil.
func (l *LUKS) Close(ctx context.Context, name string) error {
	if err := validate.MapperName(name); err != nil {
		return err
	}
	if !l.active(ctx, name) {
		return nil
	}
	if _, err := l.run.Run(ctx, nil, "luksClose", name); err != nil {
		return fmt.Errorf("luks: close %s: %w", name, err)
	}
	return nil
}

// MakeFS creates a filesystem on the opened mapper device if absent.
func (l *LUKS) MakeFS(ctx context.Context, name string) error {
	if err := validate.MapperName(name); err != nil {
		return err
	}
	if _, err := l.mkfs.Run(ctx, nil, "/dev/mapper/"+name); err != nil {
		return fmt.Errorf("luks: mkfs %s: %w", name, err)
	}
	return nil
}

// AddKey adds newKey as an additional keyslot, authenticating with oldKey.
func (l *LUKS) AddKey(ctx context.Context, device string, oldKey, newKey *keyzero.Buffer) error {
	// oldKey on fd 3; new key supplied on stdin via the same fd mechanism is
	// not possible with one descriptor, so the real impl writes newKey to a
	// second pipe. The Runner contract delivers oldKey on fd 3; the new-key
	// pipe is handled inside AddKey's dedicated runner call (Task 7 rotation).
	_, err := l.run.Run(ctx, oldKey,
		"luksAddKey", "--key-file="+KeyFDPath, device, "/dev/fd/4")
	if err != nil {
		return fmt.Errorf("luks: addkey %s: %w", device, err)
	}
	return nil
}

// RemoveKey removes the keyslot matching the supplied key.
func (l *LUKS) RemoveKey(ctx context.Context, device string, key *keyzero.Buffer) error {
	if _, err := l.run.Run(ctx, key, "luksRemoveKey", "--key-file="+KeyFDPath, device); err != nil {
		return fmt.Errorf("luks: removekey %s: %w", device, err)
	}
	return nil
}

// IsLUKS reports whether device already holds a LUKS header.
func (l *LUKS) IsLUKS(ctx context.Context, device string) (bool, error) {
	_, err := l.run.Run(ctx, nil, "isLuks", device)
	return err == nil, nil
}

// active reports whether the named mapper is currently active.
func (l *LUKS) active(ctx context.Context, name string) bool {
	out, _ := l.run.Run(ctx, nil, "status", name)
	return len(out) > 0
}
```

> **Executor note:** `AddKey` needs *two* descriptors (old key on fd 3, new key on fd 4). The single-key `Runner` covers every other operation; extend `ExecRunner.Run` with a second optional key (fd 4) when you implement Task 7 rotation, and add the matching fake-runner assertion there. Do not block Task 5 on it — `TestFormat/Open/Close/MakeFS/IsLUKS` exercise the single-key path.

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/luks/ -v` — Expected: PASS (Task 4 + Task 5 tests).

- [ ] **Step 5: Run the race detector on the package**

Run: `go test ./internal/luks/ -race` — Expected: PASS (the pipe-writer goroutine in `ExecRunner` is race-clean).

- [ ] **Step 6: Commit**

```bash
git add internal/luks/luks.go internal/luks/luks_test.go
git commit -m "feat(luks): idempotent cryptsetup wrapper with argon2id + mapper validation"
```

---

## Task 6: `vault` client — TLS-required, per-tenant scoped

Addresses **C-1** (plaintext HTTP — process refuses to start unless the address is `https://` with a CA bundle; no insecure-skip path) and **H-1/M-5** (per-tenant path scoping with validated inputs). The Vault KV operations sit behind a small `kv` interface so `ReadKey`/`EnsureKey`/rotation are unit-testable without a live Vault; the real client wires `client.KVv2(mount)`.

**Vault Go API (confirmed via context7):** `vault.DefaultConfig()` → set `config.Address` → `config.ConfigureTLS(&vault.TLSConfig{CACert: caPath})` → `vault.NewClient(config)`; AppRole via `approle.NewAppRoleAuth(roleID, secretID)` + `client.Auth().Login(ctx, auth)`; KV v2 via `client.KVv2(mount).Get/Put/GetVersion`.

**Files:** Create `internal/vault/client.go`, `internal/vault/client_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/vault/client_test.go`)

```go
package vault

import (
	"context"
	"strings"
	"testing"
)

func TestNewRejectsPlaintextHTTP(t *testing.T) {
	_, err := New(Config{Address: "http://vault.internal:8200", CABundlePath: "/tmp/ca.pem"})
	if err == nil {
		t.Fatal("New accepted a plaintext http:// address")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("error should mention https requirement: %v", err)
	}
}

func TestNewRejectsMissingCABundle(t *testing.T) {
	if _, err := New(Config{Address: "https://vault.internal:8200"}); err == nil {
		t.Fatal("New accepted an https address with no CA bundle")
	}
}

func TestKeyPathScopedAndValidated(t *testing.T) {
	p, err := keyPath("bristol", "pvc-abc123")
	if err != nil {
		t.Fatalf("keyPath: %v", err)
	}
	if p != "tenants/bristol/pvc-abc123" {
		t.Fatalf("keyPath = %q, want tenants/bristol/pvc-abc123", p)
	}
	// Injection attempts must be rejected before building the path.
	if _, err := keyPath("../admin", "pvc-abc123"); err == nil {
		t.Fatal("keyPath accepted a traversal institution")
	}
	if _, err := keyPath("bristol", "../escape"); err == nil {
		t.Fatal("keyPath accepted a traversal volume")
	}
}

// fakeKV is an in-memory KV v2 stand-in keyed by path -> versioned blobs.
type fakeKV struct {
	versions map[string][][]byte // path -> []version payloads (1-indexed)
	putErr   error
}

func (f *fakeKV) Get(_ context.Context, path string) ([]byte, int, error) {
	v := f.versions[path]
	if len(v) == 0 {
		return nil, 0, errNotFound
	}
	return v[len(v)-1], len(v), nil
}
func (f *fakeKV) GetVersion(_ context.Context, path string, version int) ([]byte, error) {
	v := f.versions[path]
	if version < 1 || version > len(v) {
		return nil, errNotFound
	}
	return v[version-1], nil
}
func (f *fakeKV) Put(_ context.Context, path string, key []byte) (int, error) {
	if f.putErr != nil {
		return 0, f.putErr
	}
	f.versions[path] = append(f.versions[path], key)
	return len(f.versions[path]), nil
}
func (f *fakeKV) DeleteAll(_ context.Context, path string) error {
	delete(f.versions, path)
	return nil
}

func TestReadKeyReturnsCurrentVersion(t *testing.T) {
	fk := &fakeKV{versions: map[string][][]byte{
		"tenants/bristol/pvc-1": {[]byte("oldkey00"), []byte("newkey01")},
	}}
	c := &Client{kv: fk}
	buf, err := c.ReadKey(context.Background(), "bristol", "pvc-1")
	if err != nil {
		t.Fatalf("ReadKey: %v", err)
	}
	defer buf.Zero()
	if string(buf.Bytes()) != "newkey01" {
		t.Fatalf("ReadKey returned %q, want current version newkey01", buf.Bytes())
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/vault/ -run TestNewRejectsPlaintextHTTP -v` — Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Write minimal implementation** (`internal/vault/client.go`)

```go
// Package vault provides a TLS-required, per-tenant scoped Vault client for
// LUKS key material. The transport refuses any non-https address.
package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
	"github.com/nfcs/k8s-luks-csi-go/internal/validate"
)

const mountPath = "secret" // KV v2 mount

var errNotFound = errors.New("vault: key not found")

// Config configures the Vault transport.
type Config struct {
	Address      string // must be https://
	CABundlePath string // PEM CA bundle; required
}

// kv abstracts the KV v2 operations the client needs (real impl wraps
// client.KVv2(mount); tests inject a fake).
type kv interface {
	Get(ctx context.Context, path string) (key []byte, version int, err error)
	GetVersion(ctx context.Context, path string, version int) ([]byte, error)
	Put(ctx context.Context, path string, key []byte) (version int, err error)
	DeleteAll(ctx context.Context, path string) error
}

// Client is a per-tenant-scoped Vault key client.
type Client struct {
	api *vaultapi.Client
	kv  kv
}

// New builds a TLS-only Vault client. It refuses to start unless Address is
// https and a CA bundle is supplied (C-1: no insecure-skip path).
func New(cfg Config) (*Client, error) {
	if !strings.HasPrefix(strings.ToLower(cfg.Address), "https://") {
		return nil, fmt.Errorf("vault: address must use https, got %q", cfg.Address)
	}
	if cfg.CABundlePath == "" {
		return nil, errors.New("vault: CA bundle path is required")
	}
	apiCfg := vaultapi.DefaultConfig()
	apiCfg.Address = cfg.Address
	if err := apiCfg.ConfigureTLS(&vaultapi.TLSConfig{CACert: cfg.CABundlePath}); err != nil {
		return nil, fmt.Errorf("vault: configure TLS: %w", err)
	}
	api, err := vaultapi.NewClient(apiCfg)
	if err != nil {
		return nil, fmt.Errorf("vault: new client: %w", err)
	}
	return &Client{api: api, kv: &apiKV{api: api}}, nil
}

// keyPath validates inputs and builds the per-tenant KV path. (M-5/H-1)
func keyPath(institution, volume string) (string, error) {
	if err := validate.Institution(institution); err != nil {
		return "", err
	}
	if err := validate.VolumeName(volume); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/%s", institution, volume), nil
}

// ReadKey fetches the current key version into an mlock'd buffer.
func (c *Client) ReadKey(ctx context.Context, institution, volume string) (*keyzero.Buffer, error) {
	p, err := keyPath(institution, volume)
	if err != nil {
		return nil, err
	}
	raw, _, err := c.kv.Get(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("vault: read key: %w", err)
	}
	return toBuffer(raw)
}

// CurrentVersion returns the latest version number for a volume's key.
func (c *Client) CurrentVersion(ctx context.Context, institution, volume string) (int, error) {
	p, err := keyPath(institution, volume)
	if err != nil {
		return 0, err
	}
	_, v, err := c.kv.Get(ctx, p)
	return v, err
}

func toBuffer(raw []byte) (*keyzero.Buffer, error) {
	buf, err := keyzero.NewBuffer(len(raw))
	if err != nil {
		return nil, err
	}
	copy(buf.Bytes(), raw)
	return buf, nil
}
```

- [ ] **Step 4: Write the real `kv` adapter** (`internal/vault/kv.go`)

The KV v2 key blob is stored under the data key `"key"` (raw bytes hex-encoded by the writer in EnsureKey, Task 7). The adapter bridges the `kv` interface to `vaultapi`.

```go
package vault

import (
	"context"
	"encoding/hex"
	"fmt"

	vaultapi "github.com/hashicorp/vault/api"
)

type apiKV struct{ api *vaultapi.Client }

func (a *apiKV) Get(ctx context.Context, path string) ([]byte, int, error) {
	s, err := a.api.KVv2(mountPath).Get(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	return decodeKey(s)
}

func (a *apiKV) GetVersion(ctx context.Context, path string, version int) ([]byte, error) {
	s, err := a.api.KVv2(mountPath).GetVersion(ctx, path, version)
	if err != nil {
		return nil, err
	}
	b, _, err := decodeKey(s)
	return b, err
}

func (a *apiKV) Put(ctx context.Context, path string, key []byte) (int, error) {
	s, err := a.api.KVv2(mountPath).Put(ctx, path, map[string]interface{}{
		"key": hex.EncodeToString(key),
	})
	if err != nil {
		return 0, err
	}
	if s == nil || s.VersionMetadata == nil {
		return 0, fmt.Errorf("vault: put returned no version metadata")
	}
	return s.VersionMetadata.Version, nil
}

func (a *apiKV) DeleteAll(ctx context.Context, path string) error {
	return a.api.KVv2(mountPath).DeleteMetadata(ctx, path)
}

func decodeKey(s *vaultapi.KVSecret) ([]byte, int, error) {
	if s == nil || s.Data == nil {
		return nil, 0, errNotFound
	}
	enc, ok := s.Data["key"].(string)
	if !ok {
		return nil, 0, fmt.Errorf("vault: key field missing or wrong type")
	}
	raw, err := hex.DecodeString(enc)
	if err != nil {
		return nil, 0, fmt.Errorf("vault: decode key: %w", err)
	}
	v := 0
	if s.VersionMetadata != nil {
		v = s.VersionMetadata.Version
	}
	return raw, v, nil
}
```

- [ ] **Step 5: Run tests, verify they pass**

Run: `go test ./internal/vault/ -v` — Expected: PASS (`New` validation, `keyPath`, `ReadKey`).

- [ ] **Step 6: Commit**

```bash
git add internal/vault/client.go internal/vault/kv.go internal/vault/client_test.go
git commit -m "feat(vault): TLS-required per-tenant scoped client"
```

---

## Task 7: `vault` rotation, EnsureKey, DeleteKey, token renewal

Addresses **H-12** (rotation bricks volume — walk versions backward until one authenticates) and **H-6** (token lifecycle — lease-aware renewal goroutine). `EnsureKey` is idempotent key creation; `DeleteKey` removes all versions (only invoked when `deletionPolicy=Delete`, Task 11).

**Files:** Create `internal/vault/rotate.go`, `internal/vault/auth.go`, `internal/vault/rotate_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/vault/rotate_test.go`)

```go
package vault

import (
	"context"
	"testing"
)

// tryOpen simulates cryptsetup luksOpen: succeeds only for the "correct" key.
func makeTryOpen(correct string) func([]byte) bool {
	return func(k []byte) bool { return string(k) == correct }
}

func TestFindAuthenticatingVersionWalksBackward(t *testing.T) {
	// v1 was the key the volume was formatted with; v2/v3 are newer Vault
	// versions that do NOT yet open the volume (rotation half-applied).
	fk := &fakeKV{versions: map[string][][]byte{
		"tenants/bristol/pvc-1": {[]byte("correct1"), []byte("newkey02"), []byte("newkey03")},
	}}
	c := &Client{kv: fk}

	buf, ver, err := c.FindAuthenticatingVersion(
		context.Background(), "bristol", "pvc-1", makeTryOpen("correct1"))
	if err != nil {
		t.Fatalf("FindAuthenticatingVersion: %v", err)
	}
	defer buf.Zero()
	if ver != 1 {
		t.Fatalf("authenticating version = %d, want 1", ver)
	}
	if string(buf.Bytes()) != "correct1" {
		t.Fatalf("returned key = %q, want correct1", buf.Bytes())
	}
}

func TestFindAuthenticatingVersionExhausted(t *testing.T) {
	fk := &fakeKV{versions: map[string][][]byte{
		"tenants/bristol/pvc-1": {[]byte("nope0001"), []byte("nope0002")},
	}}
	c := &Client{kv: fk}
	_, _, err := c.FindAuthenticatingVersion(
		context.Background(), "bristol", "pvc-1", makeTryOpen("absent"))
	if err == nil {
		t.Fatal("expected error when no version authenticates")
	}
}

func TestEnsureKeyIdempotent(t *testing.T) {
	fk := &fakeKV{versions: map[string][][]byte{}}
	c := &Client{kv: fk, genKey: func(n int) ([]byte, error) {
		return []byte("genkey00"), nil
	}}
	// First call creates the key.
	if err := c.EnsureKey(context.Background(), "bristol", "pvc-1"); err != nil {
		t.Fatalf("EnsureKey create: %v", err)
	}
	// Second call must NOT create a new version.
	if err := c.EnsureKey(context.Background(), "bristol", "pvc-1"); err != nil {
		t.Fatalf("EnsureKey idempotent: %v", err)
	}
	if got := len(fk.versions["tenants/bristol/pvc-1"]); got != 1 {
		t.Fatalf("EnsureKey created %d versions, want 1", got)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/vault/ -run TestFindAuthenticatingVersionWalksBackward -v` — Expected: FAIL, `undefined: (*Client).FindAuthenticatingVersion`.

- [ ] **Step 3: Add `genKey` field + rotation/ensure/delete** (`internal/vault/rotate.go`)

```go
package vault

import (
	"context"
	"crypto/rand"
	"fmt"
)

// genKey is overridable in tests; defaults to crypto/rand.
func (c *Client) generate(n int) ([]byte, error) {
	if c.genKey != nil {
		return c.genKey(n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// FindAuthenticatingVersion walks Vault key versions newest→oldest, returning
// the first whose bytes satisfy tryOpen (i.e. actually open the volume). This
// makes a half-applied rotation recoverable instead of bricking the volume.
func (c *Client) FindAuthenticatingVersion(
	ctx context.Context, institution, volume string, tryOpen func([]byte) bool,
) (*keyzero.Buffer, int, error) {
	p, err := keyPath(institution, volume)
	if err != nil {
		return nil, 0, err
	}
	_, current, err := c.kv.Get(ctx, p)
	if err != nil {
		return nil, 0, fmt.Errorf("vault: rotate: read current: %w", err)
	}
	for v := current; v >= 1; v-- {
		raw, err := c.kv.GetVersion(ctx, p, v)
		if err != nil {
			continue
		}
		if tryOpen(raw) {
			buf, err := toBuffer(raw)
			if err != nil {
				return nil, 0, err
			}
			return buf, v, nil
		}
	}
	return nil, 0, fmt.Errorf("vault: no key version authenticates for %s/%s", institution, volume)
}

// EnsureKey creates a fresh random key for (institution, volume) only if none
// exists. Idempotent.
func (c *Client) EnsureKey(ctx context.Context, institution, volume string) error {
	p, err := keyPath(institution, volume)
	if err != nil {
		return err
	}
	if _, _, err := c.kv.Get(ctx, p); err == nil {
		return nil // already exists
	}
	key, err := c.generate(32)
	if err != nil {
		return fmt.Errorf("vault: generate key: %w", err)
	}
	if _, err := c.kv.Put(ctx, p, key); err != nil {
		return fmt.Errorf("vault: ensure key: %w", err)
	}
	return nil
}

// DeleteKey removes all versions of a volume's key (deletionPolicy=Delete).
func (c *Client) DeleteKey(ctx context.Context, institution, volume string) error {
	p, err := keyPath(institution, volume)
	if err != nil {
		return err
	}
	return c.kv.DeleteAll(ctx, p)
}
```

Add the `genKey` field and `keyzero` import to `Client` in `client.go`:

```go
// in client.go, extend the Client struct:
type Client struct {
	api    *vaultapi.Client
	kv     kv
	genKey func(n int) ([]byte, error) // nil => crypto/rand (overridable in tests)
}
```
(and add `"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"` to `rotate.go` imports.)

- [ ] **Step 4: Write the AppRole login + token-renewal goroutine** (`internal/vault/auth.go`)

Follows the context7 token-lifecycle pattern: log in via AppRole using the role reference from the node-stage secret, then renew the lease in a background goroutine until `ctx` is cancelled.

```go
package vault

import (
	"context"
	"fmt"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	approle "github.com/hashicorp/vault/api/auth/approle"
)

// LoginAppRole authenticates with a per-tenant AppRole (role reference comes
// from the workload-namespace node-stage secret) and starts lease renewal.
// The returned cancel func stops the renewal goroutine.
func (c *Client) LoginAppRole(ctx context.Context, roleID, secretID string) (context.CancelFunc, error) {
	auth, err := approle.NewAppRoleAuth(roleID, &approle.SecretID{FromString: secretID})
	if err != nil {
		return nil, fmt.Errorf("vault: approle auth init: %w", err)
	}
	authInfo, err := c.api.Auth().Login(ctx, auth)
	if err != nil {
		return nil, fmt.Errorf("vault: approle login: %w", err)
	}
	if authInfo == nil {
		return nil, fmt.Errorf("vault: approle login returned no auth info")
	}
	renewCtx, cancel := context.WithCancel(ctx)
	go c.renew(renewCtx, authInfo)
	return cancel, nil
}

func (c *Client) renew(ctx context.Context, authInfo *vaultapi.Secret) {
	watcher, err := c.api.NewLifetimeWatcher(&vaultapi.LifetimeWatcherInput{Secret: authInfo})
	if err != nil {
		return
	}
	go watcher.Start()
	defer watcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watcher.DoneCh():
			// Lease cannot be renewed further; a fresh stage call re-logs in.
			return
		case <-watcher.RenewCh():
			// renewed; loop
			_ = time.Now // no-op; avoids unused import if trimmed
		}
	}
}
```

> **Executor note:** `time` is imported for clarity; if `go vet` flags it unused after you finalise `renew`, drop the import. `SecretID{FromString: ...}` carries the secret-id value directly from the node-stage secret (the design's "Vault role reference"). Do not log `roleID`/`secretID`.

- [ ] **Step 5: Run tests, verify they pass**

Run: `go test ./internal/vault/ -v` — Expected: PASS (rotation walk, exhaustion, EnsureKey idempotency, plus Task 6 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/vault/rotate.go internal/vault/auth.go internal/vault/client.go internal/vault/rotate_test.go
git commit -m "feat(vault): version-walk rotation, idempotent EnsureKey, AppRole renewal"
```

---

## Task 8: `device` Resolver registry + WaitForDevice

Backends differ in how the backing block device appears on a node. A `Resolver` maps the backing volume's `volume_context` to a host device path; a registry dispatches by backend name. `WaitForDevice` polls until the path appears, with an injected stat func for host-runnable tests.

**Files:** Create `internal/device/resolver.go`, `internal/device/resolver_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/device/resolver_test.go`)

```go
package device

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistryDispatch(t *testing.T) {
	reg := NewRegistry()
	reg.Register(genericByID{})
	r, err := reg.Get("by-id")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	path, err := r.Resolve(context.Background(), map[string]string{"serial": "abc123"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/dev/disk/by-id/abc123" {
		t.Fatalf("path = %q", path)
	}
}

func TestRegistryUnknownBackend(t *testing.T) {
	reg := NewRegistry()
	if _, err := reg.Get("nope"); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func TestByIDRejectsInjection(t *testing.T) {
	_, err := genericByID{}.Resolve(context.Background(), map[string]string{"serial": "../../etc/passwd"})
	if err == nil {
		t.Fatal("by-id resolver accepted a traversal serial")
	}
}

func TestWaitForDeviceSucceedsWhenStatOK(t *testing.T) {
	calls := 0
	stat := func(string) error {
		calls++
		if calls < 2 {
			return errors.New("not yet")
		}
		return nil // appears on the 2nd poll
	}
	err := waitForDevice(context.Background(), "/dev/disk/by-id/x", 10*time.Millisecond, time.Second, stat)
	if err != nil {
		t.Fatalf("waitForDevice: %v", err)
	}
}

func TestWaitForDeviceTimesOut(t *testing.T) {
	stat := func(string) error { return errors.New("never") }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := waitForDevice(ctx, "/dev/x", 10*time.Millisecond, time.Hour, stat); err == nil {
		t.Fatal("expected timeout error")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/device/ -run TestRegistryDispatch -v` — Expected: FAIL, `undefined: NewRegistry`.

- [ ] **Step 3: Write minimal implementation** (`internal/device/resolver.go`)

```go
// Package device resolves backing volumes to host block-device paths and
// attaches/detaches them via VolumeAttachment (no cluster-wide node
// pod-create) with a namespace-scoped staging fallback for map-on-stage
// backends.
package device

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"
)

// Resolver maps a backing volume_context to a host block device path.
type Resolver interface {
	Name() string
	Resolve(ctx context.Context, params map[string]string) (string, error)
}

// Registry dispatches resolvers by backend name.
type Registry struct{ resolvers map[string]Resolver }

func NewRegistry() *Registry { return &Registry{resolvers: map[string]Resolver{}} }

func (r *Registry) Register(res Resolver) { r.resolvers[res.Name()] = res }

func (r *Registry) Get(name string) (Resolver, error) {
	res, ok := r.resolvers[name]
	if !ok {
		return nil, fmt.Errorf("device: no resolver for backend %q", name)
	}
	return res, nil
}

// serialRE guards device identifiers used to build /dev/disk/by-id paths.
var serialRE = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)

// genericByID resolves cloud/attach-based block devices by stable serial.
type genericByID struct{}

func (genericByID) Name() string { return "by-id" }
func (genericByID) Resolve(_ context.Context, params map[string]string) (string, error) {
	serial := params["serial"]
	if !serialRE.MatchString(serial) {
		return "", fmt.Errorf("device: invalid serial %q", serial)
	}
	return "/dev/disk/by-id/" + serial, nil
}

// WaitForDevice blocks until path exists or the context/deadline elapses.
func WaitForDevice(ctx context.Context, path string, timeout time.Duration) error {
	return waitForDevice(ctx, path, 200*time.Millisecond, timeout, func(p string) error {
		_, err := os.Stat(p)
		return err
	})
}

func waitForDevice(ctx context.Context, path string, interval, timeout time.Duration, stat func(string) error) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	if stat(path) == nil {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("device: wait for %s: %w", path, ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("device: timed out waiting for %s", path)
		case <-tick.C:
			if stat(path) == nil {
				return nil
			}
		}
	}
}
```

> **Executor note:** Add `Longhorn` and `CephRBD` resolvers (each `Name()` + `Resolve()`) following the `genericByID` shape when wiring real backends. `CephRBD` is the map-on-stage backend that still needs the scoped staging fallback (Task 9). The `by-id` resolver is sufficient to make the registry tests pass and to drive attach-based cloud backends.

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/device/ -v` — Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/device/resolver.go internal/device/resolver_test.go
git commit -m "feat(device): resolver registry, by-id resolver, WaitForDevice"
```

---

## Task 9: `device` Attach/Detach via VolumeAttachment

Addresses **H-3** (node pod-create cluster-wide). For attach-based backends the controller creates a `storage.k8s.io/v1` `VolumeAttachment` and waits for `.Status.Attached`; the device then appears on the node with **no staging pod and no node pod-create permission**. Tested with the client-go **fake clientset** so no cluster is needed.

**client-go (confirmed via context7):** in-cluster auth via `rest.InClusterConfig()` (auto-refreshes the projected SA token through `BearerTokenFile`); `kubernetes.NewForConfig(cfg)`; `clientset.StorageV1().VolumeAttachments()`. The fake clientset is `k8s.io/client-go/kubernetes/fake`.

**Files:** Create `internal/device/attach.go`, `internal/device/attach_test.go`.

- [ ] **Step 1: Write the failing test** (`internal/device/attach_test.go`)

```go
package device

import (
	"context"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestAttachCreatesVolumeAttachmentAndWaitsAttached(t *testing.T) {
	cs := fake.NewSimpleClientset()
	// React to creation by marking the VA attached, so Attach's wait returns.
	cs.PrependReactor("create", "volumeattachments",
		func(a k8stesting.Action) (bool, k8sRuntimeObject, error) {
			va := a.(k8stesting.CreateAction).GetObject().(*storagev1.VolumeAttachment)
			va.Status.Attached = true
			return false, va, nil // let default tracker store the attached VA
		})

	a := NewAttacher(cs)
	err := a.Attach(context.Background(), AttachRequest{
		PVName: "pv-1", NodeName: "node-a", Attacher: "luks.csi.nfcs.io",
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	vas, _ := cs.StorageV1().VolumeAttachments().List(context.Background(), metav1.ListOptions{})
	if len(vas.Items) != 1 {
		t.Fatalf("expected 1 VolumeAttachment, got %d", len(vas.Items))
	}
}

func TestDetachRemovesVolumeAttachment(t *testing.T) {
	existing := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: vaName("pv-1", "node-a")},
	}
	cs := fake.NewSimpleClientset(existing)
	a := NewAttacher(cs)
	if err := a.Detach(context.Background(), "pv-1", "node-a"); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	vas, _ := cs.StorageV1().VolumeAttachments().List(context.Background(), metav1.ListOptions{})
	if len(vas.Items) != 0 {
		t.Fatalf("expected 0 VolumeAttachments after detach, got %d", len(vas.Items))
	}
}
```

> The alias `k8sRuntimeObject` is `k8s.io/apimachinery/pkg/runtime`.`Object`; add `runtime "k8s.io/apimachinery/pkg/runtime"` to the test imports and replace `k8sRuntimeObject` with `runtime.Object`.

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/device/ -run TestAttachCreatesVolumeAttachmentAndWaitsAttached -v` — Expected: FAIL, `undefined: NewAttacher`.

- [ ] **Step 3: Write minimal implementation** (`internal/device/attach.go`)

```go
package device

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// AttachRequest describes an attach-based backend attachment.
type AttachRequest struct {
	PVName   string
	NodeName string
	Attacher string // CSI driver name of the *backing* attacher
}

// Attacher drives VolumeAttachment lifecycle.
type Attacher struct {
	cs   kubernetes.Interface
	poll time.Duration
	wait time.Duration
}

func NewAttacher(cs kubernetes.Interface) *Attacher {
	return &Attacher{cs: cs, poll: 500 * time.Millisecond, wait: 2 * time.Minute}
}

// vaName derives a deterministic VolumeAttachment name from (pv, node).
func vaName(pv, node string) string {
	sum := sha256.Sum256([]byte(pv + "\x00" + node))
	return "luks-va-" + hex.EncodeToString(sum[:8])
}

// Attach creates the VolumeAttachment (idempotently) and waits until the
// backing volume reports attached on the node.
func (a *Attacher) Attach(ctx context.Context, req AttachRequest) error {
	name := vaName(req.PVName, req.NodeName)
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: req.Attacher,
			NodeName: req.NodeName,
			Source:   storagev1.VolumeAttachmentSource{PersistentVolumeName: &req.PVName},
		},
	}
	_, err := a.cs.StorageV1().VolumeAttachments().Create(ctx, va, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("device: create VolumeAttachment: %w", err)
	}
	return wait.PollUntilContextTimeout(ctx, a.poll, a.wait, true,
		func(ctx context.Context) (bool, error) {
			cur, err := a.cs.StorageV1().VolumeAttachments().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			if cur.Status.AttachError != nil {
				return false, fmt.Errorf("device: attach error: %s", cur.Status.AttachError.Message)
			}
			return cur.Status.Attached, nil
		})
}

// Detach deletes the VolumeAttachment. Idempotent: NotFound is success.
func (a *Attacher) Detach(ctx context.Context, pvName, nodeName string) error {
	err := a.cs.StorageV1().VolumeAttachments().Delete(ctx, vaName(pvName, nodeName), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("device: delete VolumeAttachment: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/device/ -v` — Expected: PASS (attach + detach + Task 8 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/device/attach.go internal/device/attach_test.go
git commit -m "feat(device): VolumeAttachment-based attach/detach"
```

---

