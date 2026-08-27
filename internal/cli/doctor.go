// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/ui"
	"github.com/fenandosr/mksrv/internal/workspace"
)

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type doctorResult struct {
	Healthy   bool          `json:"healthy"`
	Workspace string        `json:"workspace,omitempty"`
	Checks    []doctorCheck `json:"checks"`
}

func (a *App) runDoctor(ctx context.Context, printer ui.Printer, globals *globalOptions) error {
	result := doctorResult{Healthy: true, Checks: make([]doctorCheck, 0, 7)}
	addCheck := func(name, status, message string) {
		result.Checks = append(result.Checks, doctorCheck{Name: name, Status: status, Message: message})
		if status == "fail" {
			result.Healthy = false
		}
	}

	addCheck("platform", "pass", runtime.GOOS+"/"+runtime.GOARCH+" CLI platform supported")
	cachePath, err := engine.CachePath(fallback(a.build.Version, "dev"))
	if err != nil {
		addCheck("engine-cache", "fail", err.Error())
	} else if err := writableParent(cachePath); err != nil {
		addCheck("engine-cache", "fail", err.Error())
	} else {
		addCheck("engine-cache", "pass", cachePath)
	}

	if socket := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")); socket == "" {
		addCheck("ssh-agent", "warn", "SSH_AUTH_SOCK is not set; key-file authentication may still be used in later milestones")
	} else if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		addCheck("ssh-agent", "warn", "SSH_AUTH_SOCK does not point to an accessible socket")
	} else {
		addCheck("ssh-agent", "pass", socket)
	}

	if path, err := exec.LookPath("sops"); err != nil {
		addCheck("sops", "warn", "sops is not installed; secrets commands will require it")
	} else {
		addCheck("sops", "pass", path)
	}
	if awsCredentialHint() {
		addCheck("aws-credentials", "pass", "local AWS credential configuration detected (no network call performed)")
	} else {
		addCheck("aws-credentials", "warn", "no local AWS profile or environment credentials detected")
	}
	addCheck("terraform", "pass", "Terraform is auto-provisioned by mksrv beginning in M1; no system binary is required")

	root, discoverErr := workspace.Discover(defaultStartDirectory(), globals.Workspace)
	if discoverErr != nil {
		if errors.Is(discoverErr, workspace.ErrNotFound) {
			addCheck("workspace", "warn", "no workspace discovered; pass --workspace to inspect one")
		} else {
			addCheck("workspace", "fail", discoverErr.Error())
		}
	} else {
		result.Workspace = root
		_, report, validateErr := workspace.Validate(ctx, root, workspace.ValidateOptions{RunningVersion: fallback(a.build.Version, "dev"), AllowDowngrade: globals.AllowDowngrade})
		if validateErr != nil {
			addCheck("workspace", "fail", validateErr.Error())
		} else if !report.Valid {
			addCheck("workspace", "fail", fmt.Sprintf("%d validation findings", countErrors(report.Issues)))
		} else {
			addCheck("workspace", "pass", fmt.Sprintf("valid: %s", root))
		}
	}

	if printer.JSON {
		if err := printer.Encode(result); err != nil {
			return err
		}
	} else {
		for _, check := range result.Checks {
			switch check.Status {
			case "pass":
				printer.Success("%-16s %s", check.Name, check.Message)
			case "warn":
				printer.Warn("%-16s %s", check.Name, check.Message)
			case "fail":
				printer.Error("%-16s %s", check.Name, check.Message)
			}
		}
		if result.Healthy {
			printer.Success("doctor found no blocking local problems")
		} else {
			printer.Error("doctor found blocking local problems")
		}
	}
	if !result.Healthy {
		return &ExitError{Code: 1, Err: fmt.Errorf("doctor checks failed"), Printed: true}
	}
	return nil
}

func writableParent(target string) error {
	parent := filepath.Dir(target)
	for {
		info, err := os.Stat(parent)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("engine cache parent %s is not a directory", parent)
			}
			file, err := os.CreateTemp(parent, ".mksrv-write-test-*")
			if err != nil {
				return fmt.Errorf("engine cache parent %s is not writable: %w", parent, err)
			}
			name := file.Name()
			_ = file.Close()
			_ = os.Remove(name)
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect engine cache parent %s: %w", parent, err)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return fmt.Errorf("no existing parent for engine cache %s", target)
		}
		parent = next
	}
}

func awsCredentialHint() bool {
	for _, variable := range []string{"AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		if strings.TrimSpace(os.Getenv(variable)) != "" {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, candidate := range []string{filepath.Join(home, ".aws", "credentials"), filepath.Join(home, ".aws", "config")} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func countErrors(issues []workspace.Issue) int {
	count := 0
	for _, issue := range issues {
		if issue.Severity == "error" {
			count++
		}
	}
	return count
}
