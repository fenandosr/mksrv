// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"

	"github.com/fenandosr/mksrv/internal/ssh"
)

var _ Runner = (*LocalRunner)(nil)

// LocalRunner implements Runner on the machine the CLI itself runs on — no
// SSH, no network (ADR 0034). It assumes the same shape as a cloud host:
// system-level Quadlets and `sudo systemctl`/`sudo podman`, authorized by
// the operator's own local sudo rights.
type LocalRunner struct{}

// NewLocalRunner returns a Runner for the current machine.
func NewLocalRunner() *LocalRunner { return &LocalRunner{} }

// Run executes command through the local shell, mirroring ssh.Client.Run's
// contract (command already carries its own "sudo " prefix where needed).
func (LocalRunner) Run(ctx context.Context, command string) (ssh.Result, error) {
	return runLocal(ctx, command, nil)
}

// RunInput is Run with stdin fed from input.
func (LocalRunner) RunInput(ctx context.Context, command string, input []byte) (ssh.Result, error) {
	return runLocal(ctx, command, input)
}

func runLocal(ctx context.Context, command string, input []byte) (ssh.Result, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := ssh.Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, fmt.Errorf("local: command exited %d: %s", result.ExitCode, strings.TrimSpace(stderr.String()))
	}
	return result, fmt.Errorf("local: run %q: %w", command, err)
}

// WriteFileSudo writes content to path as root via an atomic temp-file
// rename, the same script ssh.Client.WriteFileSudo runs remotely — just
// executed locally instead of over an SSH session.
func (LocalRunner) WriteFileSudo(ctx context.Context, path string, content []byte, mode fs.FileMode) error {
	script := fmt.Sprintf(
		`set -e; p=%s; m=%s; d=$(dirname "$p"); mkdir -p "$d"; t="$p.mksrv.tmp"; cat > "$t"; chmod "$m" "$t"; mv -f "$t" "$p"`,
		shellQuote(path), fmt.Sprintf("%04o", mode.Perm()),
	)
	cmd := exec.CommandContext(ctx, "sudo", "-n", "sh", "-c", script)
	cmd.Stdin = bytes.NewReader(content)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write %s locally: %s", path, strings.TrimSpace(stderr.String()))
	}
	return nil
}
