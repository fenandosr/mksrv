// SPDX-License-Identifier: Apache-2.0

package tf

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func TestVersionIsPinnedSemver(t *testing.T) {
	if Version != "1.9.8" {
		t.Fatalf("Version = %q, want 1.9.8", Version)
	}
	if Pinned().String() != "1.9.8" {
		t.Fatalf("Pinned() = %s, want 1.9.8", Pinned())
	}
}

func TestCacheDirHonorsXDG(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", base)
	dir, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error = %v", err)
	}
	want := filepath.Join(base, "mksrv", "terraform", "1.9.8")
	if dir != want {
		t.Fatalf("CacheDir() = %q, want %q", dir, want)
	}
}

func TestLocateAcceptsMatchingOverride(t *testing.T) {
	fake := buildFakeTerraform(t)
	t.Setenv("FAKE_TF_VERSION", "1.9.8")
	t.Setenv("MKSRV_TERRAFORM", fake)

	got, err := Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate() error = %v", err)
	}
	if got != fake {
		t.Fatalf("Locate() = %q, want %q", got, fake)
	}
}

func TestLocateRejectsMismatchedOverride(t *testing.T) {
	fake := buildFakeTerraform(t)
	t.Setenv("FAKE_TF_VERSION", "1.5.7")
	t.Setenv("MKSRV_TERRAFORM", fake)

	_, err := Locate(context.Background())
	if err == nil {
		t.Fatal("Locate() expected a version-mismatch error")
	}
	var mismatch *ErrVersionMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("Locate() error = %v, want *ErrVersionMismatch", err)
	}
	if mismatch.Got != "1.5.7" || mismatch.Want != "1.9.8" {
		t.Fatalf("mismatch = %+v", mismatch)
	}
}

func TestValidateError(t *testing.T) {
	if err := validateError(&tfjson.ValidateOutput{Valid: true}); err != nil {
		t.Fatalf("validateError(valid) = %v, want nil", err)
	}
	if err := validateError(nil); err != nil {
		t.Fatalf("validateError(nil) = %v, want nil", err)
	}
	out := &tfjson.ValidateOutput{
		Valid: false,
		Diagnostics: []tfjson.Diagnostic{
			{Severity: tfjson.DiagnosticSeverityWarning, Summary: "deprecated thing"},
			{
				Severity: tfjson.DiagnosticSeverityError,
				Summary:  "Missing required argument",
				Detail:   `The argument "value" is required`,
				Range:    &tfjson.Range{Filename: "main.tf", Start: tfjson.Pos{Line: 7}},
			},
		},
	}
	err := validateError(out)
	if err == nil {
		t.Fatal("validateError(invalid) = nil, want error")
	}
	got := err.Error()
	if !strings.Contains(got, "main.tf:7") || !strings.Contains(got, "Missing required argument") {
		t.Fatalf("error = %q", got)
	}
	if strings.Contains(got, "deprecated thing") {
		t.Fatalf("error should omit warnings: %q", got)
	}
}

// buildFakeTerraform compiles testdata/faketf into a temp dir and returns the
// executable path. The build shares the module's toolchain and cache.
func buildFakeTerraform(t *testing.T) string {
	t.Helper()
	name := "terraform"
	if runtime.GOOS == "windows" {
		name = "terraform.exe"
	}
	target := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", target, "./testdata/faketf")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build faketf: %v\n%s", err, out)
	}
	return target
}
