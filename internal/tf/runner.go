// SPDX-License-Identifier: Apache-2.0

package tf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// Runner executes Terraform in one working directory. Terraform's stdout and
// stderr are streamed to the log writer supplied to NewRunner (mksrv's stderr);
// structured results are returned to the caller.
type Runner struct {
	terraform *tfexec.Terraform
}

// NewRunner binds execPath to workdir. logs receives Terraform's raw output; a
// nil writer discards it.
func NewRunner(execPath, workdir string, logs io.Writer) (*Runner, error) {
	terraform, err := tfexec.NewTerraform(workdir, execPath)
	if err != nil {
		return nil, fmt.Errorf("initialize terraform runner: %w", err)
	}
	if logs == nil {
		logs = io.Discard
	}
	terraform.SetStdout(logs)
	terraform.SetStderr(logs)
	return &Runner{terraform: terraform}, nil
}

// Raw exposes the underlying terraform-exec handle for operations not yet
// wrapped by a typed method.
func (r *Runner) Raw() *tfexec.Terraform { return r.terraform }

// Init runs `terraform init`. When backend is false the backend is skipped
// (`-backend=false`), which is what module validation uses. backendConfig
// entries become `-backend-config=<entry>` flags.
func (r *Runner) Init(ctx context.Context, backend bool, backendConfig ...string) error {
	opts := []tfexec.InitOption{tfexec.Backend(backend)}
	for _, entry := range backendConfig {
		opts = append(opts, tfexec.BackendConfig(entry))
	}
	if err := r.terraform.Init(ctx, opts...); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	return nil
}

// Validate runs `terraform validate` and returns an error describing every
// diagnostic when the configuration is invalid.
func (r *Runner) Validate(ctx context.Context) error {
	out, err := r.terraform.Validate(ctx)
	if err != nil {
		return fmt.Errorf("terraform validate: %w", err)
	}
	return validateError(out)
}

// validateError turns a terraform validate result into an error listing every
// error-severity diagnostic, or nil when the configuration is valid.
func validateError(out *tfjson.ValidateOutput) error {
	if out == nil || out.Valid {
		return nil
	}
	messages := make([]string, 0, len(out.Diagnostics))
	for _, diag := range out.Diagnostics {
		if diag.Severity != tfjson.DiagnosticSeverityError {
			continue
		}
		message := diag.Summary
		if diag.Detail != "" {
			message += ": " + diag.Detail
		}
		if diag.Range != nil && diag.Range.Filename != "" {
			message = fmt.Sprintf("%s:%d: %s", diag.Range.Filename, diag.Range.Start.Line, message)
		}
		messages = append(messages, message)
	}
	if len(messages) == 0 {
		return fmt.Errorf("terraform configuration is invalid")
	}
	return fmt.Errorf("terraform configuration is invalid: %s", strings.Join(messages, "; "))
}

// Plan runs `terraform plan`. When planPath is non-empty the binary plan is
// written there for a later Apply. varFiles become `-var-file` flags. It
// reports whether the plan contains changes.
func (r *Runner) Plan(ctx context.Context, planPath string, varFiles ...string) (bool, error) {
	opts := make([]tfexec.PlanOption, 0, len(varFiles)+1)
	if planPath != "" {
		opts = append(opts, tfexec.Out(planPath))
	}
	for _, file := range varFiles {
		opts = append(opts, tfexec.VarFile(file))
	}
	changed, err := r.terraform.Plan(ctx, opts...)
	if err != nil {
		return false, fmt.Errorf("terraform plan: %w", err)
	}
	return changed, nil
}

// Apply runs `terraform apply`. When planPath is non-empty that saved plan is
// applied and varFiles are ignored (Terraform forbids combining them);
// otherwise varFiles become `-var-file` flags and apply runs with
// auto-approve.
func (r *Runner) Apply(ctx context.Context, planPath string, varFiles ...string) error {
	var opts []tfexec.ApplyOption
	if planPath != "" {
		opts = append(opts, tfexec.DirOrPlan(planPath))
	} else {
		for _, file := range varFiles {
			opts = append(opts, tfexec.VarFile(file))
		}
	}
	if err := r.terraform.Apply(ctx, opts...); err != nil {
		return fmt.Errorf("terraform apply: %w", err)
	}
	return nil
}

// Output returns the root-module outputs as raw JSON values keyed by name.
func (r *Runner) Output(ctx context.Context) (map[string]json.RawMessage, error) {
	raw, err := r.terraform.Output(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform output: %w", err)
	}
	values := make(map[string]json.RawMessage, len(raw))
	for name, meta := range raw {
		values[name] = meta.Value
	}
	return values, nil
}
