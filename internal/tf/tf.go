// SPDX-License-Identifier: Apache-2.0

// Package tf wraps Terraform execution for mksrv. It pins one Terraform version,
// resolves (or downloads) a matching binary, and exposes a small typed surface
// over hashicorp/terraform-exec. See ADR 0008.
package tf

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hashicorp/go-version"
)

// Version is the Terraform version mksrv manages. It MUST stay in sync with the
// terraform_version pin in .github/workflows/ci.yaml.
const Version = "1.9.8"

// pinned is Version parsed once; it panics at init if Version is malformed.
var pinned = version.Must(version.NewVersion(Version))

// Pinned returns the managed Terraform version.
func Pinned() *version.Version { return pinned }

// CacheDir returns the directory that holds the mksrv-managed Terraform binary.
// It honors XDG_CACHE_HOME and otherwise falls back to the OS user cache dir.
func CacheDir() (string, error) {
	base := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if base == "" {
		var err error
		base, err = os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolve user cache directory: %w", err)
		}
	}
	return filepath.Join(base, "mksrv", "terraform", Version), nil
}

// binaryName is the platform-specific Terraform executable name.
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "terraform.exe"
	}
	return "terraform"
}
