// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/workspace"
)

func testWorkerRole() model.TenantAppRole {
	return model.TenantAppRole{Name: "lims-worker", CIDRs: []string{"100.64.0.11/32"}}
}

func TestTenantAppRoleDefaults(t *testing.T) {
	t.Parallel()
	r := testWorkerRole()
	if got := r.AppRoleName("hgstg"); got != "svc-hgstg-lims-worker" {
		t.Fatalf("AppRoleName = %q", got)
	}
	if got := r.SecretIDTTL(); got != 90 {
		t.Fatalf("SecretIDTTL default = %d, want 90", got)
	}
	if got := r.KVReadPaths(); len(got) != 1 || got[0] != "storage-*" {
		t.Fatalf("KVReadPaths default = %v", got)
	}
	if got := (model.TenantS3Bucket{}).AbortDays(); got != 3 {
		t.Fatalf("AbortDays default = %d, want 3", got)
	}
}

func TestTenantAppRolePolicyIsReadOnlyOverOwnSubtree(t *testing.T) {
	t.Parallel()
	hcl := tenantAppRolePolicyHCL("hgstg", testWorkerRole())
	want := "path \"kv/data/tenants/hgstg/storage-*\" {\n  capabilities = [\"read\"]\n}\n"
	if hcl != want {
		t.Fatalf("policy =\n%s\nwant\n%s", hcl, want)
	}
	for _, bad := range []string{"list", "update", "create", "delete", "transit", "sudo"} {
		if strings.Contains(hcl, bad) {
			t.Fatalf("policy grants %q: %s", bad, hcl)
		}
	}
}

func TestTenantAppRoleWriteArgs(t *testing.T) {
	t.Parallel()
	got := strings.Join(tenantAppRoleWriteArgs("hgstg", testWorkerRole()), " ")
	for _, want := range []string{
		"write auth/approle/role/svc-hgstg-lims-worker",
		"token_policies=svc-hgstg-lims-worker",
		"token_ttl=1h", "token_max_ttl=4h",
		"token_bound_cidrs=100.64.0.11/32",
		"secret_id_bound_cidrs=100.64.0.11/32",
		"secret_id_ttl=2160h",
		"secret_id_num_uses=0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("args missing %q: %s", want, got)
		}
	}
}

func TestStorageSecretJSONCarriesExactlyTwoFields(t *testing.T) {
	t.Parallel()
	var m map[string]string
	if err := json.Unmarshal(storageSecretJSON("AKIAEXAMPLE", "s3cr3t"), &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["access_key_id"] != "AKIAEXAMPLE" || m["secret_access_key"] != "s3cr3t" {
		t.Fatalf("secret doc = %v", m)
	}
}

func TestOperatorPolicyCoversTenantAppRoles(t *testing.T) {
	t.Parallel()
	tenants := map[string]model.Tenant{
		"hgstg": {ID: "hgstg", Stacks: []string{"openbao"}, Storage: &model.TenantStorage{AppRoles: []model.TenantAppRole{testWorkerRole()}}},
		"hg":    {ID: "hg", Stacks: []string{"openbao"}},
		// Not an openbao consumer: its roles are never created, so never granted.
		"nobao": {ID: "nobao", Storage: &model.TenantStorage{AppRoles: []model.TenantAppRole{testWorkerRole()}}},
	}
	names := tenantAppRoleNames(tenants)
	if len(names) != 1 || names[0] != "svc-hgstg-lims-worker" {
		t.Fatalf("tenantAppRoleNames = %v", names)
	}
	hcl := operatorAppRolePolicyHCL(openbaoTenantIDs(tenants), names...)
	for _, want := range []string{
		`path "auth/approle/role/svc-hgstg-lims-worker/secret-id" {`,
		`path "auth/approle/role/svc-hgstg-lims-worker/secret-id-accessor/destroy" {`,
		`path "auth/approle/role/tenant-hg/secret-id" {`,
	} {
		if !strings.Contains(hcl, want) {
			t.Fatalf("operator policy missing %q", want)
		}
	}
	// Never the role's own definition (no repointing at another policy).
	if strings.Contains(hcl, `path "auth/approle/role/svc-hgstg-lims-worker" {`) {
		t.Fatal("operator policy must not grant the role definition itself")
	}
}

func TestSecretIDMintArgsApproleMetadata(t *testing.T) {
	t.Parallel()
	got := strings.Join(secretIDMintArgs("auth/approle/role/svc-hgstg-lims-worker",
		tenantSecretIDOptions{AppRole: "lims-worker", Name: "ome", WrapTTL: time.Hour}), " ")
	for _, want := range []string{
		"auth/approle/role/svc-hgstg-lims-worker/secret-id",
		`"tier":"approle"`, `"approle":"lims-worker"`, `"name":"ome"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("args missing %q: %s", want, got)
		}
	}
	// No ttl unless asked: the role's secret_id_ttl (90d) applies.
	if strings.Contains(got, " ttl=") {
		t.Fatalf("args must not override the role ttl by default: %s", got)
	}
}

func TestTenantHasAppRole(t *testing.T) {
	t.Parallel()
	tn := model.Tenant{Storage: &model.TenantStorage{AppRoles: []model.TenantAppRole{testWorkerRole()}}}
	if !tenantHasAppRole(tn, "lims-worker") || tenantHasAppRole(tn, "other") || tenantHasAppRole(model.Tenant{}, "lims-worker") {
		t.Fatal("tenantHasAppRole mismatch")
	}
}

func TestTenantAppRolePolicyMultipleNamesWithUnderscore(t *testing.T) {
	t.Parallel()
	r := model.TenantAppRole{Name: "airflow", CIDRs: []string{"100.64.0.11/32"},
		KVRead: []string{"database-hgstg_airflow", "airflow-api"}}
	hcl := tenantAppRolePolicyHCL("hgstg", r)
	for _, want := range []string{
		"path \"kv/data/tenants/hgstg/database-hgstg_airflow\" {\n  capabilities = [\"read\"]\n}\n",
		"path \"kv/data/tenants/hgstg/airflow-api\" {\n  capabilities = [\"read\"]\n}\n",
	} {
		if !strings.Contains(hcl, want) {
			t.Fatalf("policy missing %q:\n%s", want, hcl)
		}
	}
	// An explicit list replaces the storage-* default.
	if strings.Contains(hcl, "storage-") {
		t.Fatalf("explicit kv_read must replace the default:\n%s", hcl)
	}
}

func TestAuthenticatorPublicationIsOptInAndNamed(t *testing.T) {
	t.Parallel()
	if got := authenticatorSecretName("hgstg"); got != "database-hgstg_auth" {
		t.Fatalf("authenticatorSecretName = %q", got)
	}
	mk := func(pg *model.PostgRESTConfig) model.Tenant {
		return model.Tenant{ID: "hgstg", Database: &model.TenantDatabase{PostgREST: pg}}
	}
	if mk(nil).PublishesAuthenticator() {
		t.Fatal("must be off by default")
	}
	if mk(&model.PostgRESTConfig{}).PublishesAuthenticator() {
		t.Fatal("must be off unless publish_authenticator is set")
	}
	if !mk(&model.PostgRESTConfig{PublishAuthenticator: true}).PublishesAuthenticator() {
		t.Fatal("publish_authenticator: true must publish")
	}
	// Without PostgREST the <id>_auth role does not exist, so nothing to publish.
	off := false
	if mk(&model.PostgRESTConfig{Enabled: &off, PublishAuthenticator: true}).PublishesAuthenticator() {
		t.Fatal("PostgREST disabled: no authenticator to publish")
	}
}

func TestAuthenticatorSecretFieldsUseTheAuthRole(t *testing.T) {
	t.Parallel()
	fields := strings.Join(dbSecretFields(postgresCluster{}, "prod", "hgstg", "hgstg_auth", "pw"), " ")
	for _, want := range []string{"username=hgstg_auth", "dbname=db_hgstg", "password=pw", "url=postgres://hgstg_auth:pw@"} {
		if !strings.Contains(fields, want) {
			t.Fatalf("fields missing %q: %s", want, fields)
		}
	}
}

func TestDBSecretFieldsPublishASingleHostWhenAProxyExists(t *testing.T) {
	t.Parallel()
	cluster := postgresCluster{
		Nodes:       []postgresNode{{Host: "core1", IP: "10.20.0.1"}, {Host: "core2", IP: "10.20.0.2"}},
		LeaderProxy: "proxy.example.com:5433",
	}
	fields := strings.Join(dbSecretFields(cluster, "prod", "scinugget", "scinugget_login", "pw"), " ")
	for _, want := range []string{
		"host_single=proxy.example.com:5433",
		"url_single=postgres://scinugget_login:pw@proxy.example.com:5433/db_scinugget",
		// The multi-host url stays as it was.
		"target_session_attrs=read-write",
	} {
		if !strings.Contains(fields, want) {
			t.Fatalf("fields missing %q: %s", want, fields)
		}
	}
	if strings.Contains(fields, "url_single=postgres://scinugget_login:pw@proxy.example.com:5433/db_scinugget?") {
		t.Fatalf("url_single must have no query string (the app adds its own): %s", fields)
	}
	// No proxy, or a standalone instance: nothing new is published.
	for name, c := range map[string]postgresCluster{
		"no proxy":   {Nodes: cluster.Nodes},
		"standalone": {LeaderProxy: "proxy.example.com:5433"},
	} {
		if strings.Contains(strings.Join(dbSecretFields(c, "prod", "x", "x_login", "pw"), " "), "_single") {
			t.Fatalf("%s: url_single must not be published", name)
		}
	}
}

func TestKVFieldsCurrentDetectsANewOrChangedField(t *testing.T) {
	t.Parallel()
	stored := `{"data":{"data":{"password":"pw","url":"u1","hosts":"h"}}}`
	same := []string{"password=pw", "url=u1", "hosts=h"}
	if !kvFieldsCurrent(stored, "pw", same) {
		t.Fatal("identical entry must be current")
	}
	if kvFieldsCurrent(stored, "pw", append(same, "url_single=s")) {
		t.Fatal("a field that is not stored yet must make the entry stale")
	}
	if kvFieldsCurrent(stored, "pw", []string{"password=pw", "url=u2"}) {
		t.Fatal("a changed field must make the entry stale")
	}
	if kvFieldsCurrent(stored, "other", same) {
		t.Fatal("a changed password must make the entry stale")
	}
}

func TestLeaderProxyEndpointPicksTheHostCarryingPgproxy(t *testing.T) {
	t.Parallel()
	mk := func(hosts map[string][]string) *fleet {
		d := model.Deployment{Env: "prod", Hosts: map[string]model.Host{}}
		for n, st := range hosts {
			d.Hosts[n] = model.Host{Stacks: st}
		}
		return &fleet{data: workspace.Data{Deployment: d}}
	}
	if got := mk(map[string][]string{"core1": {"postgres"}, "appd": {"database"}}).leaderProxyEndpoint(); got != "" {
		t.Fatalf("no pgproxy host = %q", got)
	}
	if got := mk(map[string][]string{"core1": {"postgres"}, "appd": {"database", "pgproxy"}}).leaderProxyEndpoint(); got != "prod-appd.prod.mksrv:5433" {
		t.Fatalf("endpoint = %q (mesh names carry the env prefix: prod-<host>.<env>.mksrv)", got)
	}
}

func TestMailAliasFileIsSortedLowercasedAndEmptyWithoutAliases(t *testing.T) {
	t.Parallel()
	mk := func(aliases ...model.TenantMailAlias) model.Tenant {
		return model.Tenant{Mail: &model.TenantMail{Hosted: true, Aliases: aliases}}
	}
	tenants := map[string]model.Tenant{
		"b": mk(model.TenantMailAlias{Address: "Zeta@B.example.com", To: []string{"Admin@b.example.com"}}),
		"a": mk(
			model.TenantMailAlias{Address: "info@a.example.com", To: []string{"z@a.example.com", "ADMIN@a.example.com"}},
			model.TenantMailAlias{Address: "app@a.example.com", To: []string{"admin@a.example.com"}},
		),
		"none": mk(),
	}
	got := mailAliasFile(tenants, []string{"a", "b", "none"})
	want := mailAliasFileHeader +
		"app@a.example.com admin@a.example.com\n" +
		"info@a.example.com admin@a.example.com z@a.example.com\n" +
		"zeta@b.example.com admin@b.example.com\n"
	if got != want {
		t.Fatalf("alias file =\n%s\nwant\n%s", got, want)
	}
	if got := mailAliasFile(tenants, []string{"none"}); got != "" {
		t.Fatalf("no aliases must render nothing, got %q", got)
	}
}

func TestStaleBackupAccessorsOnlyPicksBackupOnlyTokens(t *testing.T) {
	t.Parallel()
	got := staleBackupAccessors(map[string][]string{
		"keep":      {"backup"},
		"old1":      {"backup"},
		"old2":      {"backup"},
		"root":      {"root"},
		"mixed":     {"backup", "default"},
		"app":       {"svc-hgstg-lims-worker"},
		"nopolicy":  nil,
		"backupish": {"backup-admin"},
	}, "keep")
	want := []string{"old1", "old2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("stale = %v, want %v (never root, never a token with more than the backup policy, never the one to keep)", got, want)
	}
	if got := staleBackupAccessors(map[string][]string{"only": {"backup"}}, "only"); len(got) != 0 {
		t.Fatalf("the token to keep must survive, got %v", got)
	}
}

func TestParseTokenAuthAndRenewalSucceeded(t *testing.T) {
	t.Parallel()
	tok, acc, err := parseTokenAuth(`{"auth":{"client_token":"s.abc","accessor":"acc1"}}`)
	if err != nil || tok != "s.abc" || acc != "acc1" {
		t.Fatalf("parseTokenAuth = %q %q %v", tok, acc, err)
	}
	if _, _, err := parseTokenAuth(`{"auth":{}}`); err == nil {
		t.Fatal("a response with no client token must be an error")
	}
	for name, c := range map[string]struct {
		in   string
		want bool
	}{
		"same token back":           {`{"auth":{"client_token":"s.stored","accessor":"a","lease_duration":2592000}}`, true},
		"a different token":         {`{"auth":{"client_token":"s.other","accessor":"a","lease_duration":2592000}}`, false},
		"no token, accessor, lease": {`{"auth":{"accessor":"a","lease_duration":2592000}}`, true},
		"no token, zero lease":      {`{"auth":{"accessor":"a","lease_duration":0}}`, false},
		"empty auth":                {`{"auth":{}}`, false},
		"not json":                  {`permission denied`, false},
	} {
		if got := renewalSucceeded(c.in, "s.stored"); got != c.want {
			t.Errorf("%s: renewalSucceeded = %v, want %v", name, got, c.want)
		}
	}
}
