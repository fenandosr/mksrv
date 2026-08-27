// SPDX-License-Identifier: Apache-2.0

package tf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	install "github.com/hashicorp/hc-install"
	"github.com/hashicorp/hc-install/fs"
	"github.com/hashicorp/hc-install/product"
	"github.com/hashicorp/hc-install/releases"
	"github.com/hashicorp/hc-install/src"
	"github.com/hashicorp/terraform-exec/tfexec"
)

// ErrVersionMismatch is returned when a candidate Terraform binary does not
// report the pinned Version.
type ErrVersionMismatch struct {
	Path string
	Got  string
	Want string
}

func (e *ErrVersionMismatch) Error() string {
	return fmt.Sprintf("terraform at %s is version %s, need %s", e.Path, e.Got, e.Want)
}

// Locate resolves a Terraform executable of the pinned Version. Resolution
// order (ADR 0008):
//
//  1. MKSRV_TERRAFORM (must report the pinned version exactly);
//  2. a binary previously downloaded into CacheDir;
//  3. a matching terraform already on PATH;
//  4. download the pinned release into CacheDir (requires network).
//
// Steps 1–3 work offline.
func Locate(ctx context.Context) (string, error) {
	if override := strings.TrimSpace(os.Getenv("MKSRV_TERRAFORM")); override != "" {
		if err := checkVersion(ctx, override); err != nil {
			return "", fmt.Errorf("MKSRV_TERRAFORM: %w", err)
		}
		return override, nil
	}

	cacheDir, err := CacheDir()
	if err != nil {
		return "", err
	}
	cached := filepath.Join(cacheDir, binaryName())
	if _, statErr := os.Stat(cached); statErr == nil {
		if err := checkVersion(ctx, cached); err == nil {
			return cached, nil
		}
		// A stale or corrupt cache entry falls through to reinstallation.
	}

	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("create terraform cache directory: %w", err)
	}
	installer := install.NewInstaller()
	execPath, err := installer.Ensure(ctx, []src.Source{
		&fs.ExactVersion{
			Product:    product.Terraform,
			Version:    pinned,
			ExtraPaths: []string{cacheDir},
		},
		&releases.ExactVersion{
			Product:    product.Terraform,
			Version:    pinned,
			InstallDir: cacheDir,
		},
	})
	if err != nil {
		return "", fmt.Errorf("locate or install terraform %s: %w", Version, err)
	}
	return execPath, nil
}

// checkVersion runs `terraform version` on execPath and confirms it matches the
// pinned Version.
func checkVersion(ctx context.Context, execPath string) error {
	terraform, err := tfexec.NewTerraform(os.TempDir(), execPath)
	if err != nil {
		return err
	}
	got, _, err := terraform.Version(ctx, true)
	if err != nil {
		return err
	}
	if !got.Core().Equal(pinned) {
		return &ErrVersionMismatch{Path: execPath, Got: got.String(), Want: Version}
	}
	return nil
}
