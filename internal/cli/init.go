// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fenandosr/mksrv/internal/scaffold"
	"github.com/fenandosr/mksrv/internal/ui"
	"github.com/fenandosr/mksrv/internal/workspace"
)

type initOptions struct {
	params scaffold.Params
	force  bool
}

type initResult struct {
	Workspace string   `json:"workspace"`
	Created   []string `json:"created"`
	Valid     bool     `json:"valid"`
}

func (a *App) newInitCommand(opts *globalOptions) *cobra.Command {
	local := &initOptions{}
	cmd := &cobra.Command{
		Use:   "init [PATH]",
		Short: "Scaffold a new private workspace",
		Long: `Create a new private mksrv workspace in PATH (default: the current directory,
or --workspace). Required values are taken from flags or, on a terminal,
interactive prompts. --yes skips prompts and fails if a required value is
missing. The command refuses to overwrite an existing deployment.yaml unless
--force is given, and runs the standard validation pass on the result.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runInit(cmd.Context(), a.printer(opts), opts, args, local)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&local.params.Env, "env", "", `Environment name (default "prod")`)
	flags.StringVar(&local.params.Region, "region", "", "AWS region")
	flags.StringVar(&local.params.Profile, "profile", "", "AWS named profile (optional)")
	flags.StringVar(&local.params.RootDomain, "root-domain", "", "Operator DNS root domain")
	flags.StringVar(&local.params.MgmtCIDR, "mgmt-cidr", "", "Management network CIDR allowed to reach SSH")
	flags.StringVar(&local.params.KeycloakDomain, "keycloak-domain", "", `Keycloak FQDN (default "auth.<root-domain>")`)
	flags.StringVar(&local.params.HeadscaleDomain, "headscale-domain", "", `Headscale FQDN (default "vpn.<root-domain>")`)
	flags.StringVar(&local.params.ACMEEmail, "acme-email", "", "Contact email for ACME/Let's Encrypt")
	flags.BoolVar(&local.force, "force", false, "Overwrite an existing deployment.yaml")
	return cmd
}

func (a *App) runInit(ctx context.Context, printer ui.Printer, globals *globalOptions, args []string, local *initOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target := globals.Workspace
	if len(args) == 1 {
		target = args[0]
	}
	if strings.TrimSpace(target) == "" {
		target = defaultStartDirectory()
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return &ExitError{Code: 2, Err: fmt.Errorf("resolve target directory: %w", err)}
	}

	params := local.params
	params.Engine = fallback(a.build.Version, "dev")
	params = params.WithDefaults()

	if missing := params.MissingRequired(); len(missing) > 0 {
		if globals.Yes || !stdinIsInteractive() {
			return &ExitError{Code: 2, Err: fmt.Errorf("missing required values: %s (provide the matching --flag)", strings.Join(missing, ", "))}
		}
		if err := promptMissing(a.stdin, a.stderr, &params, missing); err != nil {
			return &ExitError{Code: 2, Err: err}
		}
		params = params.WithDefaults()
	}

	created, err := scaffold.Generate(absolute, params, local.force)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}

	_, report, err := workspace.Validate(ctx, absolute, workspace.ValidateOptions{
		RunningVersion: fallback(a.build.Version, "dev"),
		AllowDowngrade: globals.AllowDowngrade,
	})
	if err != nil {
		return err
	}

	result := initResult{Workspace: absolute, Created: created, Valid: report.Valid}
	if printer.JSON {
		if err := printer.Encode(result); err != nil {
			return err
		}
	} else {
		printer.Success("workspace scaffolded: %s", absolute)
		for _, name := range created {
			printer.Info("created %s", name)
		}
		for _, issue := range report.Issues {
			location := issue.File
			if issue.Path != "" {
				location += ":" + issue.Path
			}
			if issue.Severity == "warning" {
				printer.Warn("%s [%s] %s", location, issue.Code, issue.Message)
			} else {
				printer.Error("%s [%s] %s", location, issue.Code, issue.Message)
			}
		}
		if report.Valid {
			printer.Success("workspace valid; edit deployment.yaml and add tenants under tenants/")
		} else {
			printer.Error("scaffolded workspace is not yet valid; resolve the findings above")
		}
	}
	if !report.Valid {
		return &ExitError{Code: 1, Err: fmt.Errorf("scaffolded workspace failed validation"), Printed: true}
	}
	return nil
}

func promptMissing(stdin io.Reader, out io.Writer, params *scaffold.Params, missing []string) error {
	reader := bufio.NewReader(stdin)
	for _, name := range missing {
		fmt.Fprintf(out, "%s: ", initPromptLabel(name))
		line, err := reader.ReadString('\n')
		if err != nil && !(err == io.EOF && line != "") {
			return fmt.Errorf("read %s from prompt: %w", name, err)
		}
		value := strings.TrimSpace(line)
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
		switch name {
		case "region":
			params.Region = value
		case "root-domain":
			params.RootDomain = value
		case "mgmt-cidr":
			params.MgmtCIDR = value
		case "acme-email":
			params.ACMEEmail = value
		}
	}
	return nil
}

func initPromptLabel(name string) string {
	switch name {
	case "region":
		return "AWS region"
	case "root-domain":
		return "Operator root domain"
	case "mgmt-cidr":
		return "Management CIDR (e.g. 203.0.113.4/32)"
	case "acme-email":
		return "ACME contact email"
	default:
		return name
	}
}

func stdinIsInteractive() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
