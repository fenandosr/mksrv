// SPDX-License-Identifier: Apache-2.0

// Package cli implements the mksrv command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
	return &App{build: build, stdout: stdout, stderr: stderr}
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

// Execute parses args and runs one command.
func (a *App) Execute(ctx context.Context, args []string) error {
	globals, remaining, err := parseGlobals(args)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	printer := ui.Printer{Data: a.stdout, Human: a.stderr, JSON: globals.JSON, Quiet: globals.Quiet, NoColor: globals.NoColor}
	if len(remaining) == 0 {
		a.printHelp(printer)
		return nil
	}
	command := remaining[0]
	commandArgs := remaining[1:]
	if command == "help" || command == "--help" || command == "-h" {
		a.printHelp(printer)
		return nil
	}
	if containsHelp(commandArgs) {
		a.printCommandHelp(printer, command)
		return nil
	}

	switch command {
	case "version":
		return a.runVersion(ctx, printer, commandArgs)
	case "validate":
		return a.runValidate(ctx, printer, globals, commandArgs)
	case "doctor":
		return a.runDoctor(ctx, printer, globals, commandArgs)
	default:
		return &ExitError{Code: 2, Err: fmt.Errorf("unknown command %q", command)}
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

func parseGlobals(args []string) (globalOptions, []string, error) {
	var options globalOptions
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--workspace":
			index++
			if index >= len(args) {
				return options, nil, fmt.Errorf("--workspace requires a path")
			}
			options.Workspace = args[index]
		case strings.HasPrefix(arg, "--workspace="):
			options.Workspace = strings.TrimPrefix(arg, "--workspace=")
		case arg == "--verbose" || arg == "-v":
			options.Verbose++
		case strings.HasPrefix(arg, "-v") && arg != "-v" && !strings.HasPrefix(arg, "--"):
			for _, flag := range strings.TrimPrefix(arg, "-") {
				if flag != 'v' {
					return options, nil, fmt.Errorf("unknown shorthand flag in %q", arg)
				}
				options.Verbose++
			}
		case arg == "--quiet" || arg == "-q":
			options.Quiet = true
		case arg == "--json":
			options.JSON = true
		case arg == "--no-color":
			options.NoColor = true
		case arg == "--yes":
			options.Yes = true
		case arg == "--allow-downgrade":
			options.AllowDowngrade = true
		default:
			remaining = append(remaining, arg)
		}
	}
	if options.Quiet && options.Verbose > 0 {
		return options, nil, fmt.Errorf("--quiet and --verbose cannot be used together")
	}
	return options, remaining, nil
}

func containsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func (a *App) printHelp(printer ui.Printer) {
	if printer.JSON {
		_ = printer.Encode(map[string]any{"name": "mksrv", "commands": []string{"version", "validate", "doctor"}})
		return
	}
	fmt.Fprintln(a.stderr, `mksrv — Service stacks as code — deploy anywhere, wired by the mesh.

Usage:
  mksrv [global flags] <command> [arguments]

M0 commands:
  version                 Print build and embedded-engine information
  validate [PATH]         Validate a workspace (discovered by default)
  doctor                  Check local prerequisites and workspace health

Global flags:
  --workspace PATH        Select a workspace
  --verbose, -v           Increase diagnostic output
  --quiet, -q             Suppress successful human output
  --json                  Emit machine-readable JSON on stdout
  --no-color              Disable ANSI color
  --yes                   Assume yes for non-destructive prompts
  --allow-downgrade       Permit validation with an older binary
  --help, -h              Show help

Commands for infrastructure, deploy, tenants, users, DNS, and secrets are added
in milestones M1–M6.`)
}

func (a *App) printCommandHelp(printer ui.Printer, command string) {
	if printer.JSON {
		_ = printer.Encode(map[string]string{"command": command, "help": commandHelp(command)})
		return
	}
	fmt.Fprintln(a.stderr, commandHelp(command))
}

func commandHelp(command string) string {
	switch command {
	case "version":
		return "Usage: mksrv version [--json]"
	case "validate":
		return "Usage: mksrv validate [PATH] [--workspace PATH] [--json] [--allow-downgrade]"
	case "doctor":
		return "Usage: mksrv doctor [--workspace PATH] [--json]"
	default:
		return fmt.Sprintf("Unknown command %q", command)
	}
}

func defaultStartDirectory() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
