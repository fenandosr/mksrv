// SPDX-License-Identifier: Apache-2.0

// Package cli implements the mksrv command line on top of Cobra.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/fenandosr/mksrv/internal/ui"
)

// BuildInfo contains values stamped by cmd/mksrv through ldflags.
type BuildInfo struct {
	Version          string
	Commit           string
	Date             string
	ModulePath       string
	TerraformVersion string
}

// App is one configured CLI instance.
type App struct {
	build  BuildInfo
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// ExitError carries a stable process exit code.
type ExitError struct {
	Code    int
	Err     error
	Printed bool
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("command failed with exit code %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// New creates an App.
func New(build BuildInfo, stdout, stderr io.Writer) *App {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &App{build: build, stdin: os.Stdin, stdout: stdout, stderr: stderr}
}

// SetStdin overrides the reader used for interactive prompts. It is intended
// for tests; the default is os.Stdin.
func (a *App) SetStdin(r io.Reader) {
	if r != nil {
		a.stdin = r
	}
}

type globalOptions struct {
	Workspace      string
	Verbose        int
	Quiet          bool
	JSON           bool
	NoColor        bool
	Yes            bool
	AllowDowngrade bool
}

const rootLong = `mksrv — Service stacks as code — deploy anywhere, wired by the mesh.

Machine-readable results are written as JSON to stdout; human diagnostics stay on
stderr. Commands for infrastructure, deploy, tenants, users, DNS, and secrets are
added in milestones M1–M6.`

// Execute parses args and runs one command.
func (a *App) Execute(ctx context.Context, args []string) error {
	opts := &globalOptions{}
	root := a.newRootCommand(opts)
	root.SetArgs(args)
	root.SetOut(a.stderr)
	root.SetErr(a.stderr)

	err := root.ExecuteContext(ctx)
	if err == nil {
		return nil
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return err
	}
	return &ExitError{Code: 2, Err: err}
}

func (a *App) newRootCommand(opts *globalOptions) *cobra.Command {
	root := &cobra.Command{
		Use:               "mksrv",
		Short:             "Service stacks as code — deploy anywhere, wired by the mesh.",
		Long:              rootLong,
		SilenceUsage:      true,
		SilenceErrors:     true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if opts.Quiet && opts.Verbose > 0 {
				return fmt.Errorf("--quiet and --verbose cannot be used together")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	flags := root.PersistentFlags()
	flags.StringVar(&opts.Workspace, "workspace", "", "Select a workspace")
	flags.CountVarP(&opts.Verbose, "verbose", "v", "Increase diagnostic output")
	flags.BoolVarP(&opts.Quiet, "quiet", "q", false, "Suppress successful human output")
	flags.BoolVar(&opts.JSON, "json", false, "Emit machine-readable JSON on stdout")
	flags.BoolVar(&opts.NoColor, "no-color", false, "Disable ANSI color")
	flags.BoolVar(&opts.Yes, "yes", false, "Assume yes for non-destructive prompts")
	flags.BoolVar(&opts.AllowDowngrade, "allow-downgrade", false, "Permit validation with an older binary")

	root.AddCommand(
		a.newVersionCommand(opts),
		a.newInitCommand(opts),
		a.newValidateCommand(opts),
		a.newDoctorCommand(opts),
		a.newPlanCommand(opts),
		a.newApplyCommand(opts),
		a.newUnlockCommand(opts),
		a.newDestroyCommand(opts),
		a.newHostCommand(opts),
		a.newBootstrapCommand(opts),
		a.newDeployCommand(opts),
		a.newMeshCommand(opts),
		a.newTenantCommand(opts),
		a.newUsersCommand(opts),
		a.newStatusCommand(opts),
	)
	return root
}

func (a *App) newVersionCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build and embedded-engine information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runVersion(cmd.Context(), a.printer(opts))
		},
	}
}

func (a *App) newValidateCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "validate [PATH]",
		Short: "Validate a workspace (discovered by default)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runValidate(cmd.Context(), a.printer(opts), opts, args)
		},
	}
}

func (a *App) newDoctorCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check local prerequisites and workspace health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runDoctor(cmd.Context(), a.printer(opts), opts)
		},
	}
}

func (a *App) printer(opts *globalOptions) ui.Printer {
	return ui.Printer{
		Data:    a.stdout,
		Human:   a.stderr,
		JSON:    opts.JSON,
		Quiet:   opts.Quiet,
		NoColor: opts.NoColor,
	}
}

// ExitCode extracts a process exit code from an error.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *ExitError
	if errors.As(err, &exitError) && exitError.Code > 0 {
		return exitError.Code
	}
	return 1
}

// AlreadyPrinted reports whether command output already explained an error.
func AlreadyPrinted(err error) bool {
	var exitError *ExitError
	return errors.As(err, &exitError) && exitError.Printed
}

func defaultStartDirectory() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
