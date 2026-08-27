// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	engineassets "github.com/fenandosr/mksrv"
)

func TestEmbeddedSchemasAreValidJSON(t *testing.T) {
	t.Parallel()
	entries, err := fs.Glob(engineassets.FS, "schemas/*.json")
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("schema count = %d, want 4", len(entries))
	}
	for _, name := range entries {
		data, err := engineassets.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", name, err)
		}
		var schema map[string]any
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatalf("json.Unmarshal(%s) error = %v", name, err)
		}
		if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			t.Fatalf("%s $schema = %#v", name, schema["$schema"])
		}
	}
}

func TestValidateTenant(t *testing.T) {
	t.Parallel()
	validator := New()
	valid := []byte(`
version: 1
id: acme
display_name: ACME Corp
base_domain: acme.example.com
stacks: [database, files]
device_limit: 60
`)
	_, issues, err := validator.ValidateYAML("tenant.v1.json", valid)
	if err != nil {
		t.Fatalf("ValidateYAML() error = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %#v", issues)
	}
	invalid := []byte(strings.Replace(string(valid), "id: acme", "id: ACME!", 1))
	_, issues, err = validator.ValidateYAML("tenant.v1.json", invalid)
	if err != nil {
		t.Fatalf("ValidateYAML() error = %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("expected validation issue")
	}
}

func TestValidateAssertsStringFormats(t *testing.T) {
	t.Parallel()
	document := []byte(`
version: 1
tenant: acme
users:
  - email: not-an-email
`)
	_, issues, err := New().ValidateYAML("users.v1.json", document)
	if err != nil {
		t.Fatalf("ValidateYAML() error = %v", err)
	}
	found := false
	for _, issue := range issues {
		if issue.Keyword == "format" && strings.HasPrefix(issue.Path, "$.users[0].email") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an email format issue, got %#v", issues)
	}
}
