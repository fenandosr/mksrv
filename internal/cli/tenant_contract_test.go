// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// contractWorkspace is the example workspace plus a tenant that exercises every
// section of the contract. It has no .mksrv directory: no outputs, no state.
func contractWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(exampleWorkspace())); err != nil {
		t.Fatal(err)
	}
	tenant := `version: 1
id: demo
display_name: "Demo"
base_domain: demo.example.com
dns_override:
  provider: route53
  zone_id: ZEXAMPLEDEMOZONEID
keycloak:
  realm: demo
  clients:
    - id: demo-web
      redirect_uris: [https://app.demo.example.com/callback]
      claims: {groups: true}
stacks: [database, openbao, cache]
database:
  postgrest: true
  schema: api
  service_roles:
    demo_worker: {}
storage:
  s3:
    bucket: demo-example-data
  approles:
    - name: app
      cidrs: [100.64.0.9/32]
      kv_read: [database, "app-*"]
`
	if err := os.WriteFile(filepath.Join(dir, "tenants", "demo.yaml"), []byte(tenant), 0o644); err != nil {
		t.Fatal(err)
	}
	// Give the example fleet the stacks the tenant consumes, on a cluster with a
	// leader proxy.
	deployment := filepath.Join(dir, "deployment.yaml")
	b, err := os.ReadFile(deployment)
	if err != nil {
		t.Fatal(err)
	}
	var cores strings.Builder
	for i, stacks := range []string{"agent, postgres, openbao, cache, pgproxy", "agent, postgres, openbao", "agent, postgres, openbao"} {
		fmt.Fprintf(&cores, "  core%d:\n    provider: aws\n    instance_type: t4g.large\n    root_gb: 30\n    data_gb: 40\n    stacks: [%s]\n", i+1, stacks)
	}
	updated := strings.Replace(string(b), "\ntelemetry:", cores.String()+"\ntelemetry:", 1)
	if err := os.WriteFile(deployment, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func snapshotFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		out[path] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func runContract(t *testing.T, dir, id string) (tenantContract, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	app := New(BuildInfo{Version: "dev"}, &out, &errb)
	err := app.Execute(context.Background(), []string{"tenant", "contract", id, "--workspace", dir})
	var c tenantContract
	if err == nil {
		if jerr := json.Unmarshal(out.Bytes(), &c); jerr != nil {
			t.Fatalf("not JSON: %v\n%s", jerr, out.String())
		}
	}
	return c, out.String(), err
}

func TestTenantContractIsOfflineAndWritesNothing(t *testing.T) {
	t.Parallel()
	dir := contractWorkspace(t)
	before := snapshotFiles(t, dir)
	c, first, err := runContract(t, dir, "demo")
	if err != nil {
		t.Fatalf("contract: %v", err)
	}
	if !reflect.DeepEqual(before, snapshotFiles(t, dir)) {
		t.Fatal("the command changed the workspace")
	}
	if _, second, _ := runContract(t, dir, "demo"); second != first {
		t.Fatal("the output must be deterministic")
	}
	if c.ContractVersion != contractVersion || c.Tenant != "demo" {
		t.Fatalf("header: %+v", c)
	}
}

func TestTenantContractMatchesWhatApplyBuilds(t *testing.T) {
	t.Parallel()
	c, raw, err := runContract(t, contractWorkspace(t), "demo")
	if err != nil {
		t.Fatalf("contract: %v", err)
	}

	for _, want := range []string{`CREATE ROLE mksrv_web`} {
		if !strings.Contains(c.Database.GlobalSQL, want) {
			t.Errorf("global SQL lacks %q", want)
		}
	}
	for _, want := range []string{"mksrv.pgrst_pre_request", "'demo_worker'", "'demo_auth'", contractPassword, contractAuthPassword, contractServicePassword("demo_worker")} {
		if !strings.Contains(c.Database.TenantSQL, want) {
			t.Errorf("tenant SQL lacks %q", want)
		}
	}

	policy := c.OpenBao.Policies["svc-demo-app"]
	if !strings.Contains(policy, `kv/data/tenants/demo/app-*`) || strings.Contains(policy, "list") {
		t.Errorf("app policy = %s", policy)
	}
	if got := c.OpenBao.AppRoles["svc-demo-app"]["token_bound_cidrs"]; got != "100.64.0.9/32" {
		t.Errorf("token_bound_cidrs = %q", got)
	}
	if got := c.OpenBao.AppRoles["tenant-demo"]["token_policies"]; got != "tenant-demo-dev" {
		t.Errorf("tenant-demo policy = %q", got)
	}
	if len(c.OpenBao.OIDCRoles) != 2 {
		t.Errorf("oidc roles = %v", c.OpenBao.OIDCRoles)
	}

	for entry, want := range map[string][]string{
		"storage-s3": {"access_key_id", "secret_access_key"},
		"cache":      {"host", "password", "url", "username"},
	} {
		if !slices.Equal(c.Secrets[entry], want) {
			t.Errorf("%s fields = %v, want %v", entry, c.Secrets[entry], want)
		}
	}
	for _, entry := range []string{"database", "database-demo_worker"} {
		if !slices.Contains(c.Secrets[entry], "url") || !slices.Contains(c.Secrets[entry], "url_single") {
			t.Errorf("%s fields = %v", entry, c.Secrets[entry])
		}
	}

	mappers := map[string][]string{}
	for _, cl := range c.Keycloak.Clients {
		id, _ := cl.Representation["clientId"].(string)
		for _, m := range cl.Mappers {
			name, _ := m["name"].(string)
			mappers[id] = append(mappers[id], name)
		}
		if _, ok := mappers[id]; !ok {
			mappers[id] = nil
		}
	}
	if !slices.Equal(mappers["demo-web"], []string{"mksrv-groups"}) {
		t.Errorf("demo-web mappers = %v", mappers["demo-web"])
	}
	if !slices.Contains(mappers["openbao"], "mksrv-audience") {
		t.Errorf("openbao mappers = %v", mappers["openbao"])
	}
	if c.Keycloak.Realm["registrationAllowed"] != false {
		t.Errorf("realm = %v", c.Keycloak.Realm)
	}

	if !strings.HasPrefix(c.Cache.ACL, "user demo on >"+contractPassword) || !strings.Contains(c.Cache.Config, "allkeys-lru") {
		t.Errorf("cache = %+v", c.Cache)
	}
	if !strings.Contains(c.PostgREST.Unit, "PGRST_DB_SCHEMAS=api") || !strings.Contains(c.PostgREST.Unit, "__PRIVATE_IP__") {
		t.Errorf("unit:\n%s", c.PostgREST.Unit)
	}
	if c.StorageS3.Bucket != "demo-example-data" || c.Images["database"]["postgrest"] == "" {
		t.Errorf("storage/images: %+v %v", c.StorageS3, c.Images)
	}
	// Another tenant's declarations never appear.
	if strings.Contains(raw, "acme") {
		t.Error("the contract of demo mentions acme")
	}
}

func TestTenantContractOmitsWhatTheTenantDoesNotUse(t *testing.T) {
	t.Parallel()
	c, _, err := runContract(t, contractWorkspace(t), "acme")
	if err != nil {
		t.Fatalf("contract: %v", err)
	}
	if c.OpenBao != nil || c.Secrets != nil || c.Cache != nil || c.PostgREST != nil {
		t.Errorf("acme declares no openbao, cache or PostgREST: %+v", c)
	}
}

func TestTenantContractUnknownTenantExitsTwo(t *testing.T) {
	t.Parallel()
	_, out, err := runContract(t, contractWorkspace(t), "nobody")
	if ExitCode(err) != 2 || out != "" {
		t.Fatalf("ExitCode = %d, out = %q, err = %v", ExitCode(err), out, err)
	}
}
