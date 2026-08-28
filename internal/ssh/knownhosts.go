// SPDX-License-Identifier: Apache-2.0

package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrUnknownHostKey means the host is not in the workspace known_hosts file.
// The operator must enroll it with `mksrv host trust`.
var ErrUnknownHostKey = errors.New("host key not trusted")

func hostKeyCallback(knownHostsPath string) (ssh.HostKeyCallback, error) {
	if strings.TrimSpace(knownHostsPath) == "" {
		return nil, errors.New("known_hosts path is required")
	}
	if _, err := os.Stat(knownHostsPath); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0o700); err != nil {
			return nil, fmt.Errorf("create known_hosts directory: %w", err)
		}
		if err := os.WriteFile(knownHostsPath, nil, 0o600); err != nil {
			return nil, fmt.Errorf("create known_hosts file: %w", err)
		}
	}
	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts %s: %w", knownHostsPath, err)
	}
	return callback, nil
}

func isHostKeyError(err error) bool {
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		return true
	}
	return strings.Contains(err.Error(), "knownhosts: key is unknown") ||
		strings.Contains(err.Error(), "knownhosts: key mismatch")
}

// FetchHostKey opens an unauthenticated connection to target solely to read its
// advertised host key.
func FetchHostKey(ctx context.Context, target Target) (ssh.PublicKey, error) {
	var captured ssh.PublicKey
	config := &ssh.ClientConfig{
		User: target.User,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = key
			return nil
		},
		Auth:    []ssh.AuthMethod{},
		Timeout: 15 * time.Second,
	}
	dialer := net.Dialer{Timeout: config.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", target.addr())
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", target.addr(), err)
	}
	defer conn.Close()
	sshConn, _, _, err := ssh.NewClientConn(conn, target.addr(), config)
	if sshConn != nil {
		_ = sshConn.Close()
	}
	if captured == nil {
		return nil, fmt.Errorf("no host key offered by %s: %w", target.addr(), err)
	}
	return captured, nil
}

// TrustResult reports the outcome of enrolling one host.
type TrustResult struct {
	Host        string
	Fingerprint string
	Added       bool
}

// Trust fetches target's host key and appends it to knownHostsPath if not
// already present. It returns an error if a different key is already recorded.
func Trust(ctx context.Context, knownHostsPath string, target Target) (TrustResult, error) {
	key, err := FetchHostKey(ctx, target)
	if err != nil {
		return TrustResult{Host: target.Host}, err
	}
	result := TrustResult{Host: target.Host, Fingerprint: ssh.FingerprintSHA256(key)}

	if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0o700); err != nil {
		return result, err
	}
	existing, err := os.ReadFile(knownHostsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}

	hostPattern := knownhosts.Normalize(target.addr())
	line := knownhosts.Line([]string{hostPattern}, key)

	for _, existingLine := range strings.Split(string(existing), "\n") {
		existingLine = strings.TrimSpace(existingLine)
		if existingLine == "" || strings.HasPrefix(existingLine, "#") {
			continue
		}
		if strings.Contains(existingLine, hostPattern) {
			if existingLine == line {
				return result, nil // already trusted, same key
			}
			return result, fmt.Errorf("%s already has a different host key in %s; remove the stale line to re-enroll", target.Host, knownHostsPath)
		}
	}

	file, err := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return result, err
	}
	defer file.Close()
	if _, err := file.WriteString(line + "\n"); err != nil {
		return result, err
	}
	result.Added = true
	return result, nil
}
