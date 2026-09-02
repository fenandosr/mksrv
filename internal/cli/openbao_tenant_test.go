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
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("policy missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "tenants/other") || strings.Count(got, "tenants/acme/") != 2 {
		t.Fatalf("policy not scoped to one tenant:\n%s", got)
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
