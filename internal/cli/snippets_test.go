// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/workspace"
)

func snippetExampleWorkspace() string {
	return filepath.Clean(filepath.Join("..", "..", "examples", "workspace"))
}

// TestEverySnippetPassesWorkspaceValidation goes beyond the schema: each default snippet is put
// into a copy of the example workspace and the WHOLE workspace must still validate (cross-file
// and semantic rules included), which is what "valid as inserted" means to a person.
func TestEverySnippetPassesWorkspaceValidation(t *testing.T) {
	t.Parallel()
	put := func(doc map[string]any, path string, body any) {
		var keys []string
		segs := strings.Split(strings.Trim(path, "/"), "/")
		for i := 0; i < len(segs); i++ {
			if segs[i] == "properties" && i+1 < len(segs) {
				keys = append(keys, segs[i+1])
				i++
			}
		}
		cur := doc
		for _, k := range keys[:len(keys)-1] {
			next, ok := cur[k].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[k] = next
			}
			cur = next
		}
		cur[keys[len(keys)-1]] = body
	}
	for _, schemaName := range []string{"deployment", "tenant"} {
		for path, list := range engine.Snippets(schemaName) {
			for _, sn := range list {
				dir := t.TempDir()
				if err := os.CopyFS(dir, os.DirFS(snippetExampleWorkspace())); err != nil {
					t.Fatal(err)
				}
				var body any
				if err := json.Unmarshal([]byte(sn.Body), &body); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(sn.Label, "approle") {
					// Documented precondition of this snippet: the tenant lists `openbao`, and some host carries it.
					for _, f := range []struct{ file, key string }{{"tenants/acme.yaml", "stacks"}} {
						addStack(t, filepath.Join(dir, f.file), nil, f.key, "openbao")
					}
				}
				file := filepath.Join(dir, "deployment.yaml")
				if schemaName == "tenant" {
					file = filepath.Join(dir, "tenants", "acme.yaml")
				}
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := yaml.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				switch {
				case schemaName == "tenant" && path == "":
					// A skeleton is a whole new tenant: write it as its own file.
					newDoc, _ := body.(map[string]any)
					newDoc["id"], newDoc["base_domain"] = "newco", "newco.example.com"
					out, _ := yaml.Marshal(newDoc)
					if err := os.WriteFile(filepath.Join(dir, "tenants", "newco.yaml"), out, 0o644); err != nil {
						t.Fatal(err)
					}
					doc = nil
				case schemaName == "deployment":
					hosts, _ := doc["hosts"].(map[string]any)
					for k, v := range body.(map[string]any) {
						hosts[k] = v
					}
				default:
					put(doc, path, body)
				}
				if doc != nil {
					out, _ := yaml.Marshal(doc)
					if err := os.WriteFile(file, out, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				_, report, err := workspace.Validate(context.Background(), dir, workspace.ValidateOptions{RunningVersion: "dev", AllowDowngrade: true})
				if err != nil {
					t.Errorf("%s snippet %q: %v", schemaName, sn.Label, err)
					continue
				}
				if strings.Contains(sn.Label, "approle") {
					// The example fleet has no openbao cluster (3 cloud nodes), and the snippet's description
					// says the tenant needs that stack. That one error is the precondition, anything else is the snippet's.
					for _, is := range report.Issues {
						if is.Severity == "error" && is.Code != "tenant.stack.unassigned" {
							t.Errorf("%s snippet %q: unexpected error %s: %s", schemaName, sn.Label, is.Code, is.Message)
						}
					}
					continue
				}
				if !report.Valid {
					t.Errorf("%s snippet %q (at %q) makes the workspace invalid: %+v", schemaName, sn.Label, path, report)
				}
			}
		}
	}
}

// addStack appends a stack name to the list at path.key of a YAML file.
func addStack(t *testing.T, file string, path []string, key, stack string) {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	cur := doc
	for _, k := range path {
		cur = cur[k].(map[string]any)
	}
	cur[key] = append(cur[key].([]any), stack)
	out, _ := yaml.Marshal(doc)
	if err := os.WriteFile(file, out, 0o644); err != nil {
		t.Fatal(err)
	}
}
