// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/model"
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

// TestMaterializeTenantsHomogeneousKeys guards a real Terraform break: `var.tenants`
// is a `map(object({...optional(...)}))`, and — separately — every element of a
// list(object(...)) it contains must carry the same key set as its siblings.
// Before this fix, a Tenant/TenantWebEndpoint/TenantForward `omitempty` field
// that only some tenants (or some entries within one tenant's list) populated
// produced a plan-time "all map elements must have the same type" once a
// second tenant, or a second heterogeneous list entry, actually diverged.
func TestMaterializeTenantsHomogeneousKeys(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	data := workspace.Data{
		Root: root,
		Deployment: model.Deployment{
			Version: 1, Engine: "dev", Env: "prod",
			MgmtCIDR: "10.0.0.0/8",
			AWS:      model.AWSConfig{Region: "us-east-1"},
			Backend:  model.BackendConfig{Type: "s3", Bucket: "b", DynamoDBTable: "t"},
			DNS:      model.DNSConfig{Provider: "route53", RootDomain: "example.com"},
			Identity: model.IdentityConfig{KeycloakDomain: "auth.example.com", HeadscaleDomain: "vpn.example.com", ACMEEmail: "ops@example.com"},
			Hosts:    map[string]model.Host{"edge": {Provider: "aws", Stacks: []string{"base"}}},
		},
		Tenants: map[string]model.Tenant{
			// No web/forwards/database at all — the sparse case.
			"bare": {Version: 1, ID: "bare", DisplayName: "Bare", BaseDomain: "bare.example.com", Stacks: []string{"monitor"}},
			// Two forwards entries with different optional fields populated, and
			// two web entries where only one sets sso_groups — the exact shape
			// that broke `terraform plan` for the MCPS web-forward rollout.
			"rich": {
				Version: 1, ID: "rich", DisplayName: "Rich", BaseDomain: "rich.example.com", Stacks: []string{"monitor"},
				Forwards: []model.TenantForward{
					{ID: "ssh1", Label: "SSH", Type: "ssh", Target: "h:22", SSHAlias: "h", Open: "ssh-terminal"},
					{ID: "tcp1", Label: "TCP", Type: "tcp", Target: "h:2222"},
				},
				Web: []model.TenantWebEndpoint{
					{Hostname: "a.rich.example.com", Target: "h:80", SSO: true},
					{Hostname: "b.rich.example.com", Target: "h:80", SSO: true, SSOGroups: []string{"dev"}},
				},
			},
		},
	}

	varsPath, err := Materialize(data, nil)
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	varsRaw, err := os.ReadFile(varsPath)
	if err != nil {
		t.Fatal(err)
	}
	var vars struct {
		Tenants map[string]map[string]json.RawMessage `json:"tenants"`
	}
	if err := json.Unmarshal(varsRaw, &vars); err != nil {
		t.Fatalf("tfvars not valid JSON: %v", err)
	}

	keySet := func(m map[string]json.RawMessage) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	var listKeys func(t *testing.T, raw json.RawMessage, label string)
	listKeys = func(t *testing.T, raw json.RawMessage, label string) {
		t.Helper()
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatalf("%s: not a JSON array: %v", label, err)
		}
		if len(entries) < 2 {
			return
		}
		want := keySet(entries[0])
		for i, e := range entries[1:] {
			if got := keySet(e); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s[%d] keys = %v, want %v (every list(object(...)) element must share one key set)", label, i+1, got, want)
			}
		}
	}

	rich, ok := vars.Tenants["rich"]
	if !ok {
		t.Fatalf("tenants missing rich: %v", vars.Tenants)
	}
	listKeys(t, rich["forwards"], "rich.forwards")
	listKeys(t, rich["web"], "rich.web")
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
