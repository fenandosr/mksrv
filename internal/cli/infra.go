// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	awsclient "github.com/fenandosr/mksrv/internal/aws"
	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/infra"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/tf"
	"github.com/fenandosr/mksrv/internal/ui"
	"github.com/fenandosr/mksrv/internal/workspace"
)

func (a *App) newPlanCommand(opts *globalOptions) *cobra.Command {
	var infraOnly bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show the infrastructure changes an apply would make",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !infraOnly {
				return &ExitError{Code: 2, Err: fmt.Errorf("only --infra-only is implemented; host bootstrap and deploy arrive in M2")}
			}
			return a.runInfraPlan(cmd.Context(), a.printer(opts), opts)
		},
	}
	cmd.Flags().BoolVar(&infraOnly, "infra-only", false, "Plan only the Terraform-managed infrastructure")
	return cmd
}

func (a *App) newUnlockCommand(opts *globalOptions) *cobra.Command {
	var infraOnly bool
	cmd := &cobra.Command{
		Use:   "unlock LOCK_ID",
		Short: "Release a stale Terraform state lock",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !infraOnly {
				return &ExitError{Code: 2, Err: fmt.Errorf("pass --infra-only; it is the only state mksrv locks today")}
			}
			session, err := a.openInfraSession(cmd.Context(), a.printer(opts), opts)
			if err != nil {
				return err
			}
			if err := session.runner.Raw().ForceUnlock(cmd.Context(), args[0]); err != nil {
				return fmt.Errorf("force-unlock %s: %w", args[0], err)
			}
			a.printer(opts).Success("released state lock %s", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&infraOnly, "infra-only", false, "Release the infrastructure state lock")
	return cmd
}

func (a *App) newApplyCommand(opts *globalOptions) *cobra.Command {
	var infraOnly, trustHosts bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Create or update infrastructure, then bootstrap and deploy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.runInfraApply(cmd.Context(), a.printer(opts), opts); err != nil {
				return err
			}
			if infraOnly {
				return nil
			}
			return a.runFleetApply(cmd.Context(), a.printer(opts), opts, trustHosts)
		},
	}
	cmd.Flags().BoolVar(&infraOnly, "infra-only", false, "Stop after the Terraform-managed infrastructure")
	cmd.Flags().BoolVar(&trustHosts, "trust-hosts", false, "Enroll unknown host keys automatically (first run)")
	return cmd
}

type infraSession struct {
	data     workspace.Data
	runner   *tf.Runner
	varsPath string
	planPath string
	stateDir string
}

func (a *App) openInfraSession(ctx context.Context, printer ui.Printer, globals *globalOptions) (*infraSession, error) {
	root, err := workspace.Discover(defaultStartDirectory(), globals.Workspace)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return nil, &ExitError{Code: 2, Err: fmt.Errorf("%w; pass --workspace", err)}
		}
		return nil, err
	}
	data, report, err := workspace.Validate(ctx, root, workspace.ValidateOptions{
		RunningVersion: fallback(a.build.Version, "dev"),
		AllowDowngrade: globals.AllowDowngrade,
	})
	if err != nil {
		return nil, err
	}
	if !report.Valid {
		for _, issue := range report.Issues {
			if issue.Severity == "error" {
				printer.Error("%s:%s [%s] %s", issue.File, issue.Path, issue.Code, issue.Message)
			}
		}
		return nil, &ExitError{Code: 1, Err: fmt.Errorf("workspace is invalid; run mksrv validate"), Printed: true}
	}
	dep := data.Deployment

	clients, err := awsclient.Load(ctx, awsclient.Options{Region: dep.AWS.Region, Profile: dep.AWS.Profile})
	if err != nil {
		return nil, &ExitError{Code: 2, Err: err}
	}
	identity, err := clients.WhoAmI(ctx)
	if err != nil {
		return nil, &ExitError{Code: 2, Err: fmt.Errorf("AWS credentials are not usable: %w", err)}
	}
	printer.Info("AWS account %s as %s in %s", identity.Account, identity.ARN, clients.Region())

	awsEnv, err := clients.ExportEnv(ctx)
	if err != nil {
		return nil, &ExitError{Code: 2, Err: err}
	}

	status, err := clients.EnsureBackend(ctx, awsclient.BackendSpec{
		Bucket:        dep.Backend.Bucket,
		DynamoDBTable: dep.Backend.DynamoDBTable,
		Region:        infra.BackendRegion(dep),
	})
	if err != nil {
		return nil, &ExitError{Code: 2, Err: fmt.Errorf("state backend: %w", err)}
	}
	if status.BucketCreated {
		printer.Success("created state bucket %s (versioned, encrypted, private)", status.Bucket)
	}
	if status.TableCreated {
		printer.Success("created state lock table %s", status.Table)
	}

	cacheDir, err := engine.Extract(ctx, fallback(a.build.Version, "dev"))
	if err != nil {
		return nil, err
	}
	workdir := filepath.Join(cacheDir, "infra", "root")

	extra := map[string]any{}
	if key := operatorPublicKey(); key != "" {
		extra["ssh_public_key"] = key
	} else {
		printer.Warn("no SSH public key found (MKSRV_SSH_PUBLIC_KEY or ~/.ssh/id_ed25519.pub); hosts will be reachable only through SSM")
	}
	varsPath, err := infra.Materialize(data, extra)
	if err != nil {
		return nil, err
	}

	execPath, err := tf.Locate(ctx)
	if err != nil {
		return nil, err
	}
	runner, err := tf.NewRunner(execPath, workdir, a.stderr)
	if err != nil {
		return nil, err
	}

	stateDir := infra.WorkDir(data.Root)
	pluginCache, err := terraformPluginCacheDir()
	if err != nil {
		return nil, err
	}
	overrides := map[string]string{
		"TF_DATA_DIR":         filepath.Join(stateDir, "tfdata"),
		"TF_PLUGIN_CACHE_DIR": pluginCache,
	}
	for key, value := range awsEnv {
		overrides[key] = value
	}
	childEnv := envWith(overrides)
	delete(childEnv, "AWS_PROFILE")
	if err := runner.Raw().SetEnv(childEnv); err != nil {
		return nil, fmt.Errorf("configure terraform environment: %w", err)
	}

	printer.Info("terraform init")
	if err := runner.Init(ctx, true, infra.BackendConfig(dep)...); err != nil {
		return nil, err
	}
	return &infraSession{
		data:     data,
		runner:   runner,
		varsPath: varsPath,
		planPath: filepath.Join(stateDir, "plan.bin"),
		stateDir: stateDir,
	}, nil
}

func (a *App) runInfraPlan(ctx context.Context, printer ui.Printer, globals *globalOptions) error {
	session, err := a.openInfraSession(ctx, printer, globals)
	if err != nil {
		return err
	}
	changed, err := session.runner.Plan(ctx, session.planPath, session.varsPath)
	if err != nil {
		return err
	}
	if printer.JSON {
		return printer.Encode(map[string]any{"changed": changed, "plan": session.planPath})
	}
	if changed {
		printer.Success("plan written to %s; run mksrv apply --infra-only to apply it", session.planPath)
	} else {
		printer.Success("infrastructure matches the workspace; no changes")
	}
	return nil
}

func (a *App) runInfraApply(ctx context.Context, printer ui.Printer, globals *globalOptions) error {
	session, err := a.openInfraSession(ctx, printer, globals)
	if err != nil {
		return err
	}

	changed, err := session.runner.Plan(ctx, session.planPath, session.varsPath)
	if err != nil {
		return err
	}
	if !changed {
		printer.Success("infrastructure already matches the workspace; nothing to apply")
		return a.writeInfraOutputs(ctx, printer, session)
	}

	if !globals.Yes {
		if !stdinIsInteractive() {
			return &ExitError{Code: 2, Err: fmt.Errorf("refusing to apply without confirmation; pass --yes")}
		}
		fmt.Fprint(a.stderr, "Apply these infrastructure changes? [y/N]: ")
		answer, _ := bufio.NewReader(a.stdin).ReadString('\n')
		if !isAffirmative(answer) {
			return &ExitError{Code: 1, Err: fmt.Errorf("apply cancelled"), Printed: false}
		}
	}

	printer.Info("terraform apply")
	if err := session.runner.Apply(ctx, session.planPath); err != nil {
		return err
	}
	if err := a.writeInfraOutputs(ctx, printer, session); err != nil {
		return err
	}
	if err := updateLock(session.data.Root, fallback(a.build.Version, "dev")); err != nil {
		printer.Warn("infrastructure applied but mksrv.lock update failed: %v", err)
	}
	printer.Success("infrastructure applied")
	return nil
}

func (a *App) writeInfraOutputs(ctx context.Context, printer ui.Printer, session *infraSession) error {
	outputs, err := session.runner.Output(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(session.stateDir, "outputs.json")
	blob, err := json.MarshalIndent(outputs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if printer.JSON {
		return printer.Encode(map[string]any{"outputs": json.RawMessage(blob), "outputs_file": path})
	}
	if hosts, ok := outputs["hosts"]; ok {
		printer.Info("hosts: %s", string(hosts))
	}
	printer.Success("outputs written to %s", path)
	return nil
}

func updateLock(root, engineVersion string) error {
	lock := model.Lock{Engine: engineVersion, AppliedAt: time.Now().UTC().Format(time.RFC3339)}
	blob, err := yaml.Marshal(lock)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "mksrv.lock"), blob, 0o600)
}

func operatorPublicKey() string {
	if raw := strings.TrimSpace(os.Getenv("MKSRV_SSH_PUBLIC_KEY")); raw != "" {
		if strings.HasPrefix(raw, "ssh-") {
			return raw
		}
		if contents, err := os.ReadFile(expandHome(raw)); err == nil {
			return strings.TrimSpace(string(contents))
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, name := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
		if contents, err := os.ReadFile(filepath.Join(home, ".ssh", name)); err == nil {
			return strings.TrimSpace(string(contents))
		}
	}
	return ""
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func terraformPluginCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	dir := filepath.Join(base, "mksrv", "terraform-plugins")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create terraform plugin cache: %w", err)
	}
	return dir, nil
}

// envWith returns the process environment as a map with overrides applied.
// terraform-exec's SetEnv replaces the child environment entirely, so the full
// set must be supplied.
func envWith(overrides map[string]string) map[string]string {
	result := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found {
			result[key] = value
		}
	}
	for key, value := range overrides {
		result[key] = value
	}
	return result
}

func isAffirmative(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
