// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/fenandosr/mksrv/internal/ui"
	"github.com/fenandosr/mksrv/internal/workspace"
)

func (a *App) runValidate(ctx context.Context, printer ui.Printer, globals *globalOptions, args []string) error {
	if len(args) > 1 {
		return &ExitError{Code: 2, Err: fmt.Errorf("validate accepts at most one PATH argument")}
	}
	explicit := globals.Workspace
	if len(args) == 1 {
		explicit = args[0]
	}
	root, err := workspace.Discover(defaultStartDirectory(), explicit)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return &ExitError{Code: 2, Err: fmt.Errorf("%w; pass PATH or --workspace", err)}
		}
		return err
	}
	_, report, err := workspace.Validate(ctx, root, workspace.ValidateOptions{RunningVersion: fallback(a.build.Version, "dev"), AllowDowngrade: globals.AllowDowngrade})
	if err != nil {
		return err
	}
	if printer.JSON {
		if err := printer.Encode(report); err != nil {
			return err
		}
	} else {
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
			printer.Success("workspace valid: %s", report.Workspace)
			printer.Info("checked %d files; %d hosts, %d tenants, %d users, %d catalog stacks", report.FilesChecked, report.Hosts, report.Tenants, report.Users, report.CatalogStacks)
		} else {
			printer.Error("workspace is invalid: %s", report.Workspace)
		}
	}
	if !report.Valid {
		return &ExitError{Code: 1, Err: fmt.Errorf("workspace validation failed"), Printed: true}
	}
	return nil
}
