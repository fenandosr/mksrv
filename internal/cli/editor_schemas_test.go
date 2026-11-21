// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/schema"
)

func TestSchemaNameAndHeaderFollowTheFilePath(t *testing.T) {
	t.Parallel()
	for rel, want := range map[string]struct{ name, header string }{
		"deployment.yaml":         {"deployment", "# yaml-language-server: $schema=.mksrv/schemas/deployment.json"},
		"tenants/acme.yaml":       {"tenant", "# yaml-language-server: $schema=../.mksrv/schemas/tenant.json"},
		"tenants/acme.users.yaml": {"users", "# yaml-language-server: $schema=../.mksrv/schemas/users.json"},
		"mksrv.lock":              {"", ""},
		"tenants/.gitkeep":        {"", ""},
		"docs/notes.yaml":         {"", ""},
		"tenants/nested/odd.yaml": {"tenant", "# yaml-language-server: $schema=../../.mksrv/schemas/tenant.json"},
	} {
		if got := schemaNameForFile(rel); got != want.name {
			t.Errorf("schemaNameForFile(%q) = %q, want %q", rel, got, want.name)
		}
		if got := schemaHeader(rel); got != want.header {
			t.Errorf("schemaHeader(%q) = %q, want %q", rel, got, want.header)
		}
	}
}

func TestHasSchemaHeaderOnlyLooksAtTheTop(t *testing.T) {
	t.Parallel()
	if !hasSchemaHeader("# yaml-language-server: $schema=.mksrv/schemas/deployment.json\nversion: 1\n") {
		t.Fatal("a header on line 1 must be found")
	}
	if !hasSchemaHeader("# my notes\n\n# yaml-language-server: $schema=x.json\nversion: 1\n") {
		t.Fatal("a header below a comment must be found")
	}
	if hasSchemaHeader("version: 1\nenv: prod\n") {
		t.Fatal("no header must not be found")
	}
	deep := strings.Repeat("a: 1\n", 20) + "# yaml-language-server: $schema=x.json\n"
	if hasSchemaHeader(deep) {
		t.Fatal("a comment far down the file is not a header")
	}
}

func TestRefreshEditorSchemasWritesThenStaysQuiet(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	n, err := refreshEditorSchemas(root, catalog)
	if err != nil || n != len(workspaceSchemas) {
		t.Fatalf("first refresh wrote %d (err=%v), want %d", n, err, len(workspaceSchemas))
	}
	for _, name := range workspaceSchemas {
		raw, err := os.ReadFile(filepath.Join(root, ".mksrv", "schemas", name+".json"))
		if err != nil {
			t.Fatalf("%s.json not written: %v", name, err)
		}
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			t.Fatalf("%s.json is not JSON", name)
		}
	}
	if n, err := refreshEditorSchemas(root, catalog); err != nil || n != 0 {
		t.Fatalf("second refresh rewrote %d files (err=%v); unchanged content must not be touched", n, err)
	}
}

func TestSchemaCommandPrintsTheEffectiveSchema(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	app := New(BuildInfo{Version: "dev"}, &stdout, &stderr)
	if err := app.Execute(context.Background(), []string{"schema", "deployment"}); err != nil {
		t.Fatalf("schema deployment: %v (stderr=%s)", err, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if !strings.Contains(stdout.String(), `"pgproxy"`) || !strings.Contains(stdout.String(), `"t4g.small"`) {
		t.Fatal("the deployment schema must carry the stack catalog and the instance-type suggestions")
	}
	stdout.Reset()
	err := app.Execute(context.Background(), []string{"schema", "nope"})
	var exit *ExitError
	if err == nil || !errorsAs(err, &exit) || exit.Code != 2 {
		t.Fatalf("an unknown schema must exit 2, got %v", err)
	}
	if err := app.Execute(context.Background(), []string{"schema"}); err == nil {
		t.Fatal("schema with no name must be a usage error")
	}
}

func TestInitWritesTheHeaderAndTheSchemasAndValidateStaysQuietAboutThem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	app := New(BuildInfo{Version: "dev"}, &stdout, &stderr)
	if err := app.Execute(context.Background(), []string{
		"init", dir, "--yes", "--region", "us-east-1", "--root-domain", "example.com",
		"--mgmt-cidr", "203.0.113.4/32", "--acme-email", "ops@example.com",
	}); err != nil {
		t.Fatalf("init: %v; stderr=%s", err, stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(dir, "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "# yaml-language-server: $schema=.mksrv/schemas/deployment.json\n") {
		t.Fatalf("deployment.yaml must start with the schema header, got:\n%.120s", body)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mksrv", "schemas", "deployment.json")); err != nil {
		t.Fatalf("init must write the schema its header points to: %v", err)
	}

	// A tenant file without a header gets exactly one hint line from validate.
	tenant := "version: 1\nid: acme\ndisplay_name: ACME\nbase_domain: acme.example.com\nstacks: []\n"
	if err := os.WriteFile(filepath.Join(dir, "tenants", "acme.yaml"), []byte(tenant), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := app.Execute(context.Background(), []string{"validate", dir}); err != nil {
		t.Fatalf("validate: %v; stderr=%s", err, stderr.String())
	}
	out := stderr.String()
	if strings.Count(out, "hint: ") != 1 || !strings.Contains(out, "tenants/acme.yaml") || !strings.Contains(out, "$schema=../.mksrv/schemas/tenant.json") {
		t.Fatalf("validate must print one hint naming the tenant file and its header, got:\n%s", out)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "tenants", "acme.yaml")); string(got) != tenant {
		t.Fatal("validate must never edit a user's file")
	}

	// With the header in place the hint disappears.
	withHeader := schemaHeader("tenants/acme.yaml") + "\n" + tenant
	if err := os.WriteFile(filepath.Join(dir, "tenants", "acme.yaml"), []byte(withHeader), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if err := app.Execute(context.Background(), []string{"validate", dir}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "hint: ") {
		t.Fatalf("no hint expected once the header exists:\n%s", stderr.String())
	}

	// JSON output stays machine-readable: no hint, still valid JSON.
	stdout.Reset()
	if err := app.Execute(context.Background(), []string{"validate", dir, "--json"}); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("--json must stay valid JSON: %v", err)
	}
}

func errorsAs(err error, target **ExitError) bool {
	for err != nil {
		if e, ok := err.(*ExitError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
