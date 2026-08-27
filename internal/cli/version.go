// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"runtime"

	"github.com/fenandosr/mksrv/internal/tf"
	"github.com/fenandosr/mksrv/internal/ui"
)

type versionResult struct {
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	BuildDate        string `json:"build_date"`
	Module           string `json:"module"`
	Go               string `json:"go"`
	Platform         string `json:"platform"`
	EmbeddedEngine   string `json:"embedded_engine"`
	TerraformVersion string `json:"terraform_version"`
}

func (a *App) runVersion(ctx context.Context, printer ui.Printer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result := versionResult{
		Version:          fallback(a.build.Version, "dev"),
		Commit:           fallback(a.build.Commit, "unknown"),
		BuildDate:        fallback(a.build.Date, "unknown"),
		Module:           fallback(a.build.ModulePath, "github.com/fenandosr/mksrv"),
		Go:               runtime.Version(),
		Platform:         runtime.GOOS + "/" + runtime.GOARCH,
		EmbeddedEngine:   fallback(a.build.Version, "dev"),
		TerraformVersion: fallback(a.build.TerraformVersion, tf.Version),
	}
	if printer.JSON {
		return printer.Encode(result)
	}
	printer.Info("mksrv %s", result.Version)
	fmt.Fprintf(a.stderr, "  commit:             %s\n", result.Commit)
	fmt.Fprintf(a.stderr, "  build date:         %s\n", result.BuildDate)
	fmt.Fprintf(a.stderr, "  module:             %s\n", result.Module)
	fmt.Fprintf(a.stderr, "  go:                 %s\n", result.Go)
	fmt.Fprintf(a.stderr, "  platform:           %s\n", result.Platform)
	fmt.Fprintf(a.stderr, "  embedded engine:    %s\n", result.EmbeddedEngine)
	fmt.Fprintf(a.stderr, "  terraform:          %s\n", result.TerraformVersion)
	return nil
}

func fallback(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}
