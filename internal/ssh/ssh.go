// SPDX-License-Identifier: Apache-2.0

// Package ssh is mksrv's SSH and SFTP transport to fleet hosts. Host keys are
// pinned in a workspace-local known_hosts file; first use is enrolled
// explicitly (ADR 0003, open question #5). Authentication uses the SSH agent
// and the operator's default private keys.
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Target identifies one host to connect to.
type Target struct {
	Host string
	Port int
	User string
}

func (t Target) addr() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(port))
}

// Client is a live SSH connection with an SFTP subsystem.
type Client struct {
	target Target
	ssh    *ssh.Client
	sftp   *sftp.Client
}

// Dial connects to target, verifying the host key against knownHostsPath.
// ErrUnknownHostKey is returned when the host is not yet enrolled.
func Dial(ctx context.Context, target Target, knownHostsPath string) (*Client, error) {
	callback, err := hostKeyCallback(knownHostsPath)
	if err != nil {
		return nil, err
	}
	auth, err := authMethods()
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User:            target.User,
		Auth:            auth,
		HostKeyCallback: callback,
		Timeout:         15 * time.Second,
	}

	dialer := net.Dialer{Timeout: config.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", target.addr())
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", target.addr(), err)
	}
	sshConn, channels, requests, err := ssh.NewClientConn(conn, target.addr(), config)
	if err != nil {
		_ = conn.Close()
		if isHostKeyError(err) {
			return nil, fmt.Errorf("%w: %s", ErrUnknownHostKey, target.Host)
		}
		return nil, fmt.Errorf("ssh handshake with %s: %w", target.addr(), err)
	}
	client := ssh.NewClient(sshConn, channels, requests)

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("start sftp on %s: %w", target.Host, err)
	}
	return &Client{target: target, ssh: client, sftp: sftpClient}, nil
}

// Close tears down the SFTP subsystem and the connection.
func (c *Client) Close() error {
	var errs []error
	if c.sftp != nil {
		errs = append(errs, c.sftp.Close())
	}
	if c.ssh != nil {
		errs = append(errs, c.ssh.Close())
	}
	return errors.Join(errs...)
}

// Result is the outcome of one remote command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes command and returns its output. A non-zero exit is reported
// through Result.ExitCode and a non-nil error.
func (c *Client) Run(ctx context.Context, command string) (Result, error) {
	session, err := c.ssh.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("open session on %s: %w", c.target.Host, err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, ctx.Err()
	case err := <-done:
		result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
		if err == nil {
			return result, nil
		}
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitStatus()
			return result, fmt.Errorf("%s: command exited %d: %s", c.target.Host, result.ExitCode, strings.TrimSpace(stderr.String()))
		}
		return result, fmt.Errorf("%s: run %q: %w", c.target.Host, command, err)
	}
}

// RunScript pipes script to `sudo bash -s` on the host.
func (c *Client) RunScript(ctx context.Context, script string) (Result, error) {
	session, err := c.ssh.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("open session on %s: %w", c.target.Host, err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	session.Stdin = strings.NewReader(script)

	done := make(chan error, 1)
	go func() { done <- session.Run("sudo -n bash -s") }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, ctx.Err()
	case err := <-done:
		result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
		if err == nil {
			return result, nil
		}
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitStatus()
		}
		return result, fmt.Errorf("%s: bootstrap script failed (exit %d): %s", c.target.Host, result.ExitCode, strings.TrimSpace(stderr.String()))
	}
}

// WriteFile uploads content to remotePath (creating parent directories) with
// mode, using a temp file and atomic rename.
func (c *Client) WriteFile(remotePath string, content []byte, mode os.FileMode) error {
	dir := filepathDir(remotePath)
	if dir != "" && dir != "." {
		if err := c.sftp.MkdirAll(dir); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	tmp := remotePath + ".mksrv.tmp"
	file, err := c.sftp.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := c.sftp.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := c.sftp.PosixRename(tmp, remotePath); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, remotePath, err)
	}
	return nil
}

// WriteFileSudo writes content to remotePath as root, creating parent
// directories, via an atomic temp-file rename. Used for paths the SSH user
// cannot write over SFTP (e.g. /etc/containers/systemd).
func (c *Client) WriteFileSudo(ctx context.Context, remotePath string, content []byte, mode os.FileMode) error {
	session, err := c.ssh.NewSession()
	if err != nil {
		return fmt.Errorf("open session on %s: %w", c.target.Host, err)
	}
	defer session.Close()

	session.Stdin = bytes.NewReader(content)
	var stderr bytes.Buffer
	session.Stderr = &stderr

	script := fmt.Sprintf(
		`set -e; p=%s; m=%s; d=$(dirname "$p"); mkdir -p "$d"; t="$p.mksrv.tmp"; cat > "$t"; chmod "$m" "$t"; mv -f "$t" "$p"`,
		shellSingleQuote(remotePath), fmt.Sprintf("%04o", mode.Perm()),
	)
	done := make(chan error, 1)
	go func() { done <- session.Run("sudo -n sh -c " + shellSingleQuote(script)) }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("write %s on %s: %s", remotePath, c.target.Host, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ReadFile downloads remotePath.
func (c *Client) ReadFile(remotePath string) ([]byte, error) {
	file, err := c.sftp.Open(remotePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(file); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Exists reports whether remotePath exists.
func (c *Client) Exists(remotePath string) (bool, error) {
	_, err := c.sftp.Stat(remotePath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func filepathDir(p string) string {
	index := strings.LastIndex(p, "/")
	if index < 0 {
		return ""
	}
	return p[:index]
}

func authMethods() ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if socket := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")); socket != "" {
		if conn, err := net.Dial("unix", socket); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}
	home, err := os.UserHomeDir()
	if err == nil {
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			key, err := os.ReadFile(filepath.Join(home, ".ssh", name))
			if err != nil {
				continue
			}
			signer, err := ssh.ParsePrivateKey(key)
			if err != nil {
				continue
			}
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}
	if len(methods) == 0 {
		return nil, errors.New("no SSH authentication available: start an agent or place a key at ~/.ssh/id_ed25519")
	}
	return methods, nil
}
