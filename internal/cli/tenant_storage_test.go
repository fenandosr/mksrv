// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fenandosr/mksrv/internal/model"
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
