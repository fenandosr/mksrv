// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/workspace"
)

func TestMaterializeWritesTerraformInputs(t *testing.T) {
	t.Parallel()
	root := copyExampleWorkspace(t)
	data, report, err := workspace.Validate(context.Background(), root, workspace.ValidateOptions{RunningVersion: "dev"})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !report.Valid {
		t.Fatalf("example workspace invalid: %#v", report.Issues)
	}

	varsPath, err := Materialize(data, nil)
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	if filepath.Dir(varsPath) != WorkDir(root) {
		t.Fatalf("varsPath = %q, want under %q", varsPath, WorkDir(root))
	}

	varsRaw, err := os.ReadFile(varsPath)
	if err != nil {
		t.Fatal(err)
	}
	var vars struct {
		Deployment struct {
			Env string `json:"env"`
		} `json:"deployment"`
		Tenants map[string]json.RawMessage `json:"tenants"`
	}
	if err := json.Unmarshal(varsRaw, &vars); err != nil {
		t.Fatalf("tfvars not valid JSON: %v", err)
	}
	if vars.Deployment.Env != "prod" {
		t.Fatalf("deployment.env = %q", vars.Deployment.Env)
	}
	if _, ok := vars.Tenants["acme"]; !ok {
		t.Fatalf("tenants missing acme: %v", vars.Tenants)
	}

	entries := BackendConfig(data.Deployment)
	joined := strings.Join(entries, " ")
	for _, want := range []string{"bucket=", "dynamodb_table=", "encrypt=true", "region="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("BackendConfig() = %v, missing %q", entries, want)
		}
	}
}

func copyExampleWorkspace(t *testing.T) string {
	t.Helper()
	source := filepath.Clean(filepath.Join("..", "..", "examples", "workspace"))
	destination := t.TempDir()
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	}); err != nil {
		t.Fatalf("copy example workspace: %v", err)
	}
	return destination
}
