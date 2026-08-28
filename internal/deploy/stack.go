// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/render"
	"github.com/fenandosr/mksrv/internal/ssh"
)

// StackResult reports what a stack deploy did on one host.
type StackResult struct {
	Stack      string   `json:"stack"`
	Changed    []string `json:"changed"`
	Unchanged  int      `json:"unchanged"`
	Restarted  []string `json:"restarted"`
	HealthOK   []string `json:"health_ok"`
	HealthFail []string `json:"health_fail"`
}

const quadletDir = "/etc/containers/systemd/"

// DeployStack renders stack for rctx, writes only the files whose content
// changed, reloads systemd and (re)starts the affected Quadlet units when a
// unit file changed, and runs the stack's HTTP/TCP health checks.
func DeployStack(ctx context.Context, client *ssh.Client, stacksRoot string, stack model.Stack, rctx render.Context) (StackResult, error) {
	result := StackResult{Stack: stack.Name}

	files, err := render.Stack(stacksRoot, stack, rctx)
	if err != nil {
		return result, err
	}

	quadletChanged := false
	for _, dst := range render.SortedPaths(files) {
		content := files[dst]
		same, err := remoteMatches(ctx, client, dst, content)
		if err != nil {
			return result, err
		}
		if same {
			result.Unchanged++
			continue
		}
		mode := fileMode(dst)
		if err := client.WriteFileSudo(ctx, dst, content, mode); err != nil {
			return result, err
		}
		result.Changed = append(result.Changed, dst)
		if strings.HasPrefix(dst, quadletDir) {
			quadletChanged = true
		}
	}

	units := quadletUnits(files)
	if quadletChanged {
		if _, err := client.Run(ctx, "sudo systemctl daemon-reload"); err != nil {
			return result, err
		}
		for _, unit := range units {
			if _, err := client.Run(ctx, "sudo systemctl restart "+unit); err != nil {
				return result, fmt.Errorf("restart %s: %w", unit, err)
			}
			result.Restarted = append(result.Restarted, unit)
		}
	} else {
		// Ensure units are running even when nothing changed.
		for _, unit := range units {
			if _, err := client.Run(ctx, "sudo systemctl start "+unit); err != nil {
				return result, fmt.Errorf("start %s: %w", unit, err)
			}
		}
	}

	for _, check := range stack.Health {
		if err := runHealthCheck(ctx, client, check); err != nil {
			result.HealthFail = append(result.HealthFail, fmt.Sprintf("%s: %v", check.App, err))
			continue
		}
		result.HealthOK = append(result.HealthOK, check.App)
	}
	if len(result.HealthFail) > 0 {
		return result, fmt.Errorf("%s health checks failed: %s", stack.Name, strings.Join(result.HealthFail, "; "))
	}
	return result, nil
}

func remoteMatches(ctx context.Context, client *ssh.Client, remotePath string, want []byte) (bool, error) {
	sum := sha256.Sum256(want)
	res, err := client.Run(ctx, fmt.Sprintf("sha256sum %s 2>/dev/null | awk '{print $1}'", shellQuote(remotePath)))
	if err != nil {
		// missing file => not a match, not an error
		return false, nil
	}
	return strings.TrimSpace(res.Stdout) == hex.EncodeToString(sum[:]), nil
}

// quadletUnits returns the systemd service names Quadlet generates for the
// rendered .container files, in a stable order.
func quadletUnits(files map[string][]byte) []string {
	var units []string
	for dst := range files {
		base := path.Base(dst)
		switch {
		case strings.HasSuffix(base, ".container"):
			units = append(units, strings.TrimSuffix(base, ".container")+".service")
		}
	}
	sort.Strings(units)
	return units
}

func fileMode(dst string) fs.FileMode {
	if strings.HasSuffix(dst, ".sh") {
		return 0o750
	}
	return 0o644
}

func runHealthCheck(ctx context.Context, client *ssh.Client, check model.StackHealth) error {
	var cmd string
	switch check.Type {
	case "http":
		port := check.Port
		if port == 0 {
			port = 80
		}
		cmd = fmt.Sprintf("curl -fsS -m 10 --retry 12 --retry-delay 5 --retry-all-errors http://127.0.0.1:%d%s -o /dev/null", port, check.Path)
	case "tcp":
		cmd = fmt.Sprintf("for i in $(seq 1 12); do (exec 3<>/dev/tcp/127.0.0.1/%d) 2>/dev/null && exit 0; sleep 5; done; exit 1", check.Port)
	case "command":
		return nil
	default:
		return fmt.Errorf("unknown health check type %q", check.Type)
	}
	_, err := client.Run(ctx, cmd)
	return err
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
