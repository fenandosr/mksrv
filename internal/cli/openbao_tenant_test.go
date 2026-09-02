// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTenantPolicyHCL(t *testing.T) {
	t.Parallel()
	got := tenantPolicyHCL("acme")
	for _, want := range []string{
		`path "kv/data/tenants/acme/*" {`,
		`capabilities = ["create", "read", "update", "delete", "list"]`,
		`path "kv/metadata/tenants/acme/*" {`,
		`capabilities = ["read", "list", "delete"]`,
		`path "transit/encrypt/acme" {`,
		`path "transit/decrypt/acme" {`,
		`path "transit/datakey/plaintext/acme" {`,
		`path "transit/keys/acme" {`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("policy missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "acme/other") || strings.Contains(got, "transit/encrypt/other") {
		t.Fatalf("policy not scoped to one tenant:\n%s", got)
	}
}

func TestOIDCArgs(t *testing.T) {
	t.Parallel()
	cfg := strings.Join(oidcConfigArgs("acme", "https://auth.example.com/realms/acme", "s3cr3t"), " ")
	for _, want := range []string{
		"write auth/oidc-acme/config",
		"oidc_discovery_url=https://auth.example.com/realms/acme",
		"oidc_client_id=openbao",
		"oidc_client_secret=s3cr3t",
		"default_role=tenant-acme",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("oidc config args missing %q: %s", want, cfg)
		}
	}
	role := strings.Join(oidcRoleArgs("acme"), " ")
	for _, want := range []string{
		"write auth/oidc-acme/role/tenant-acme",
		"token_policies=tenant-acme",
		"allowed_redirect_uris=http://localhost:8250/oidc/callback",
		"user_claim=sub",
	} {
		if !strings.Contains(role, want) {
			t.Fatalf("oidc role args missing %q: %s", want, role)
		}
	}
}

func TestTenantSecretFields(t *testing.T) {
	t.Parallel()
	db := strings.Join(tenantDBSecretFields("acme", "p-w_1"), " ")
	for _, want := range []string{
		"dbname=db_acme", "username=acme", "password=p-w_1",
		"url=postgres://acme:p-w_1@mksrv-postgres:5432/db_acme",
	} {
		if !strings.Contains(db, want) {
			t.Fatalf("db fields missing %q: %s", want, db)
		}
	}
	cache := strings.Join(tenantCacheSecretFields("acme", "p-w_1"), " ")
	if !strings.Contains(cache, "url=redis://acme:p-w_1@mksrv-redis:6379") {
		t.Fatalf("cache fields wrong: %s", cache)
	}
}

func TestBaoKVPasswordField(t *testing.T) {
	t.Parallel()
	if got := baoKVPasswordField(`{"data":{"data":{"password":"abc","url":"x"}}}`); got != "abc" {
		t.Fatalf("password = %q", got)
	}
	if got := baoKVPasswordField(`not json`); got != "" {
		t.Fatalf("expected empty on bad input, got %q", got)
	}
	if got := baoKVPasswordField(`{"data":{"data":{}}}`); got != "" {
		t.Fatalf("expected empty when absent, got %q", got)
	}
}

func TestBaoRoleAndSecretIDParse(t *testing.T) {
	t.Parallel()
	var rid baoDataRoleID
	if err := json.Unmarshal([]byte(`{"data":{"role_id":"r-123"}}`), &rid); err != nil || rid.Data.RoleID != "r-123" {
		t.Fatalf("role id = %#v (err %v)", rid, err)
	}
	var sid baoDataSecretID
	if err := json.Unmarshal([]byte(`{"data":{"secret_id":"s-abc","secret_id_accessor":"x"}}`), &sid); err != nil || sid.Data.SecretID != "s-abc" {
		t.Fatalf("secret id = %#v (err %v)", sid, err)
	}
}
