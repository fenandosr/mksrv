// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/render"
	"github.com/fenandosr/mksrv/internal/ssh"
)

// Options configures one stack deploy.
type Options struct {
	StacksRoot string            // engine cache stacks directory
	Stack      model.Stack       // descriptor
	Context    render.Context    // template data
	Secrets    map[string]string // podman-secret leaf name -> value
}

// StackResult reports what a stack deploy did on one host.
type StackResult struct {
	Stack     string   `json:"stack"`
	Changed   []string `json:"changed"`
	Unchanged int      `json:"unchanged"`
	Restarted []string `json:"restarted"`
	Hooks     []string `json:"hooks"`
	HealthOK  []string `json:"health_ok"`
}

const (
	quadletDir  = "/etc/containers/systemd/"
	stateDirFmt = "/var/lib/mksrv/stacks/%s"
)

// DeployedMarker is the path of the per-stack "deployed" marker on a host.
func DeployedMarker(stack string) string {
	return fmt.Sprintf(stateDirFmt+"/.deployed", stack)
}

// DeployedStacks lists the stacks that have a deployed marker on the host.
func DeployedStacks(ctx context.Context, client *ssh.Client) ([]string, error) {
	res, err := client.Run(ctx, "ls -1 /var/lib/mksrv/stacks/*/.deployed 2>/dev/null | awk -F/ '{print $(NF-1)}'")
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	sort.Strings(names)
	return names, nil
}

// DeployStack renders opts.Stack, pushes its podman secrets, writes only the
// files whose content changed, reloads systemd and (re)starts the affected
// Quadlet units, runs post-deploy hooks, and executes the health checks.
func DeployStack(ctx context.Context, client *ssh.Client, opts Options) (StackResult, error) {
	stack := opts.Stack
	result := StackResult{Stack: stack.Name}

	if err := pushSecrets(ctx, client, stack.Name, opts.Secrets); err != nil {
		return result, err
	}

	files, err := render.Stack(opts.StacksRoot, stack, opts.Context)
	if err != nil {
		return result, err
	}

	quadletChanged := false
	for _, dst := range render.SortedPaths(files) {
		content := files[dst]
		if same, _ := remoteMatches(ctx, client, dst, content); same {
			result.Unchanged++
			continue
		}
		if err := client.WriteFileSudo(ctx, dst, content, fileMode(dst)); err != nil {
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
		// Restarting containers on a shared podman network can leave
		// aardvark-dns with stale records; reload it so name resolution works.
		if len(units) > 0 {
			_, _ = client.Run(ctx, "sudo podman network reload --all")
		}
	} else {
		for _, unit := range units {
			if _, err := client.Run(ctx, "sudo systemctl start "+unit); err != nil {
				return result, fmt.Errorf("start %s: %w", unit, err)
			}
		}
	}

	hooks, err := runHooks(ctx, client, opts)
	if err != nil {
		return result, err
	}
	result.Hooks = hooks

	for _, check := range stack.Health {
		if err := runHealthCheck(ctx, client, check); err != nil {
			return result, fmt.Errorf("%s health check %q failed: %w", stack.Name, check.App, err)
		}
		result.HealthOK = append(result.HealthOK, check.App)
	}

	marker := fmt.Sprintf("deployed %s\n", stack.Name)
	if err := client.WriteFileSudo(ctx, DeployedMarker(stack.Name), []byte(marker), 0o644); err != nil {
		return result, err
	}
	return result, nil
}

func pushSecrets(ctx context.Context, client *ssh.Client, stackName string, values map[string]string) error {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, leaf := range names {
		secretName := "mksrv-" + stackName + "-" + leaf
		if _, err := client.RunInput(ctx,
			"sudo podman secret create --replace "+shellQuote(secretName)+" -",
			[]byte(values[leaf]),
		); err != nil {
			return fmt.Errorf("push secret %s: %w", secretName, err)
		}
	}
	return nil
}

func runHooks(ctx context.Context, client *ssh.Client, opts Options) ([]string, error) {
	var ran []string
	for _, hook := range opts.Stack.Hooks.PostDeploy {
		if hook.Scope == "per_tenant" && opts.Context.Tenant == nil {
			continue
		}
		src := filepath.Join(opts.StacksRoot, opts.Stack.Name, filepath.FromSlash(hook.Run))
		body, err := os.ReadFile(src)
		if err != nil {
			return ran, fmt.Errorf("read hook %s: %w", hook.Run, err)
		}
		dst := fmt.Sprintf(stateDirFmt+"/%s", opts.Stack.Name, path.Base(hook.Run))
		if err := client.WriteFileSudo(ctx, dst, body, 0o750); err != nil {
			return ran, err
		}
		env := fmt.Sprintf("MKSRV_ENV=%s MKSRV_STACK=%s MKSRV_HOST=%s",
			opts.Context.Env, opts.Stack.Name, opts.Context.Host.Name)
		if opts.Context.Tenant != nil {
			env += " MKSRV_TENANT=" + opts.Context.Tenant.ID
		}
		if _, err := client.Run(ctx, fmt.Sprintf("sudo env %s bash %s", env, shellQuote(dst))); err != nil {
			return ran, fmt.Errorf("hook %s: %w", hook.Run, err)
		}
		ran = append(ran, hook.Run)
	}
	return ran, nil
}

func remoteMatches(ctx context.Context, client *ssh.Client, remotePath string, want []byte) (bool, error) {
	sum := sha256.Sum256(want)
	res, err := client.Run(ctx, fmt.Sprintf("sudo sha256sum %s 2>/dev/null | awk '{print $1}'", shellQuote(remotePath)))
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(res.Stdout) == hex.EncodeToString(sum[:]), nil
}

func quadletUnits(files map[string][]byte) []string {
	var units []string
	for dst := range files {
		base := path.Base(dst)
		if strings.HasSuffix(base, ".container") {
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
		cmd = fmt.Sprintf("curl -fsS -m 10 --retry 60 --retry-delay 5 --retry-all-errors http://127.0.0.1:%d%s -o /dev/null", port, check.Path)
	case "tcp":
		cmd = fmt.Sprintf("for i in $(seq 1 60); do (exec 3<>/dev/tcp/127.0.0.1/%d) 2>/dev/null && exit 0; sleep 5; done; exit 1", check.Port)
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
