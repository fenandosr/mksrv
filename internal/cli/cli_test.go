// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestVersionJSON(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(BuildInfo{Version: "v0.1.0", Commit: "abc123", Date: "2026-01-01T00:00:00Z", ModulePath: "example.invalid/mksrv"}, &stdout, &stderr)
	if err := app.Execute(context.Background(), []string{"version", "--json"}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result versionResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if result.Version != "v0.1.0" || result.EmbeddedEngine != "v0.1.0" {
		t.Fatalf("result = %#v", result)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestValidateExampleJSON(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(BuildInfo{Version: "dev"}, &stdout, &stderr)
	root := filepath.Clean(filepath.Join("..", "..", "examples", "workspace"))
	if err := app.Execute(context.Background(), []string{"validate", root, "--json"}); err != nil {
		t.Fatalf("Execute() error = %v; stderr=%s", err, stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if result["valid"] != true {
		t.Fatalf("result = %#v", result)
	}
}

func TestConflictingGlobalFlags(t *testing.T) {
	t.Parallel()
	app := New(BuildInfo{}, &bytes.Buffer{}, &bytes.Buffer{})
	err := app.Execute(context.Background(), []string{"version", "-v", "-q"})
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode() = %d, err=%v", ExitCode(err), err)
	}
}

func TestUnknownCommandExitsTwo(t *testing.T) {
	t.Parallel()
	app := New(BuildInfo{}, &bytes.Buffer{}, &bytes.Buffer{})
	err := app.Execute(context.Background(), []string{"frobnicate"})
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode() = %d, err=%v", ExitCode(err), err)
	}
}

func TestNoArgsPrintsHelpToStderr(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	app := New(BuildInfo{}, &stdout, &stderr)
	if err := app.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("validate")) {
		t.Fatalf("stderr missing command list: %q", stderr.String())
	}
}
