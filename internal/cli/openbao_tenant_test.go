// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTenantPolicies(t *testing.T) {
	t.Parallel()
	admin := tenantAdminPolicyHCL("acme")
	for _, want := range []string{
		`path "kv/data/tenants/acme/*" {`,
		`capabilities = ["create", "read", "update", "delete", "list"]`,
		`path "kv/destroy/tenants/acme/*" {`,
		`path "transit/keys/acme/rotate" {`,
	} {
		if !strings.Contains(admin, want) {
			t.Fatalf("admin policy missing %q:\n%s", want, admin)
		}
	}

	dev := tenantDevPolicyHCL("acme")
	if !strings.Contains(dev, `path "kv/data/tenants/acme/dev/*" {`) ||
		!strings.Contains(dev, `path "transit/encrypt/acme" {`) {
		t.Fatalf("dev policy wrong:\n%s", dev)
	}
	// dev must not be able to destroy versions or rotate the key.
	if strings.Contains(dev, "kv/destroy") || strings.Contains(dev, "keys/acme/rotate") {
		t.Fatalf("dev policy is too permissive:\n%s", dev)
	}
	// the base KV grant is read-only for dev.
	base := dev[strings.Index(dev, `"kv/data/tenants/acme/*"`):]
	base = base[:strings.Index(base, "}")]
	if strings.Contains(base, "create") || strings.Contains(base, "update") {
		t.Fatalf("dev base KV grant should be read-only:\n%s", base)
	}

	for _, p := range []string{admin, dev} {
		if strings.Contains(p, "tenants/other") || strings.Contains(p, "transit/encrypt/other") {
			t.Fatalf("policy not scoped to one tenant:\n%s", p)
		}
	}
}

func TestOIDCArgs(t *testing.T) {
	t.Parallel()
	cfg := strings.Join(oidcConfigArgs("acme", "https://auth.example.com/realms/acme", "s3cr3t"), " ")
	for _, want := range []string{
		"write auth/oidc-acme/config",
		"oidc_discovery_url=https://auth.example.com/realms/acme",
		"oidc_client_secret=s3cr3t",
		"default_role=tenant-acme-dev",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("oidc config args missing %q: %s", want, cfg)
		}
	}

	if p := oidcGroupRolePath("acme", "dev"); p != "auth/oidc-acme/role/tenant-acme-dev" {
		t.Fatalf("oidc role path = %q", p)
	}
	var dev map[string]any
	if err := json.Unmarshal(oidcGroupRolePayload([]string{"dev", "admin"}, "tenant-acme-dev"), &dev); err != nil {
		t.Fatal(err)
	}
	bc, _ := dev["bound_claims"].(map[string]any)
	groups, _ := bc["groups"].([]any)
	if len(groups) != 2 || groups[0] != "dev" || dev["token_policies"] != "tenant-acme-dev" ||
		dev["allowed_redirect_uris"] != "http://localhost:8250/oidc/callback" {
		t.Fatalf("oidc dev role payload wrong: %s", oidcGroupRolePayload([]string{"dev", "admin"}, "tenant-acme-dev"))
	}
	var adm map[string]any
	_ = json.Unmarshal(oidcGroupRolePayload([]string{"admin"}, "tenant-acme-admin"), &adm)
	if adm["token_policies"] != "tenant-acme-admin" {
		t.Fatalf("oidc admin role payload wrong: %s", oidcGroupRolePayload([]string{"admin"}, "tenant-acme-admin"))
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
