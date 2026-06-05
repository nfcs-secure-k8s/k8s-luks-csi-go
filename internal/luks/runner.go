package luks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/nfcs/k8s-luks-csi-go/internal/keyzero"
)

// KeyFDPath is the path cryptsetup reads the key from. fd 3 is the first
// entry in exec.Cmd.ExtraFiles (stdin=0, stdout=1, stderr=2, extra[0]=3).
const KeyFDPath = "/dev/fd/3"

// Runner executes an external command, optionally delivering key material on
// file descriptor 3. Abstracted for unit-test injection.
type Runner interface {
	// Run executes the binary with args. If key is non-nil its bytes are
	// streamed to the child on fd 3 via os.Pipe, then the write end is closed.
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

	var wg sync.WaitGroup
	if key != nil {
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("luks: pipe: %w", err)
		}
		// Child inherits the read end as fd 3 (ExtraFiles[0]).
		cmd.ExtraFiles = []*os.File{pr}
		defer pr.Close()

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer pw.Close()
			_, _ = pw.Write(key.Bytes())
		}()
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	// Wait for the key-write goroutine to finish before returning, so the
	// caller's keyzero.Buffer.Zero() cannot race with the pipe write.
	wg.Wait()
	if runErr != nil {
		return stdout.Bytes(), fmt.Errorf("luks: %s %v: %w: %s",
			r.bin, args, runErr, stderr.String())
	}
	return stdout.Bytes(), nil
}
