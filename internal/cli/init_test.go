// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInitCreatesValidWorkspace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	app := New(BuildInfo{Version: "dev"}, &stdout, &stderr)
	err := app.Execute(context.Background(), []string{
		"init", dir, "--json", "--yes",
		"--region", "us-east-1",
		"--root-domain", "example.com",
		"--mgmt-cidr", "203.0.113.4/32",
		"--acme-email", "ops@example.com",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v; stderr=%s", err, stderr.String())
	}
	var result initResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; stdout=%s", err, stdout.String())
	}
	if !result.Valid || len(result.Created) == 0 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "deployment.yaml")); err != nil {
		t.Fatalf("deployment.yaml not written: %v", err)
	}

	var vstdout, vstderr bytes.Buffer
	validateApp := New(BuildInfo{Version: "dev"}, &vstdout, &vstderr)
	if err := validateApp.Execute(context.Background(), []string{"validate", dir, "--json"}); err != nil {
		t.Fatalf("validate on scaffolded workspace error = %v; stderr=%s", err, vstderr.String())
	}
}

func TestInitNonInteractiveMissingValuesExitsTwo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	app := New(BuildInfo{Version: "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
	err := app.Execute(context.Background(), []string{"init", dir, "--yes"})
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode() = %d, err=%v", ExitCode(err), err)
	}
}

func TestInitRefusesOverwrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	args := []string{
		"init", dir, "--yes",
		"--region", "us-east-1",
		"--root-domain", "example.com",
		"--mgmt-cidr", "203.0.113.4/32",
		"--acme-email", "ops@example.com",
	}
	first := New(BuildInfo{Version: "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err := first.Execute(context.Background(), args); err != nil {
		t.Fatalf("first init error = %v", err)
	}
	second := New(BuildInfo{Version: "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err := second.Execute(context.Background(), args); ExitCode(err) != 2 {
		t.Fatalf("expected exit 2 on overwrite, got %d (%v)", ExitCode(err), err)
	}
	third := New(BuildInfo{Version: "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err := third.Execute(context.Background(), append(args, "--force")); err != nil {
		t.Fatalf("forced init error = %v", err)
	}
}
