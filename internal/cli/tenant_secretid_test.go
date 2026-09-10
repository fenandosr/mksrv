// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"
	"time"
)

func TestSecretIDMintArgs(t *testing.T) {
	t.Parallel()
	role := "auth/approle/role/tenant-hg"

	base := strings.Join(secretIDMintArgs(role, tenantSecretIDOptions{WrapTTL: time.Hour}), " ")
	for _, want := range []string{
		"write -format=json -wrap-ttl=1h0m0s auth/approle/role/tenant-hg/secret-id",
		`metadata={"issued_by":"mksrv"}`,
	} {
		if !strings.Contains(base, want) {
			t.Fatalf("base args missing %q: %s", want, base)
		}
	}
	if strings.Contains(base, "cidr_list") || strings.Contains(base, " ttl=") || strings.Contains(base, "num_uses") {
		t.Fatalf("base args should carry no optional params: %s", base)
	}

	full := strings.Join(secretIDMintArgs(role, tenantSecretIDOptions{
		Name: "celery-prod", WrapTTL: 24 * time.Hour, TTL: 90 * 24 * time.Hour,
		NumUses: 5, CIDRs: []string{"100.64.0.0/10", "10.20.0.0/16"},
	}), " ")
	for _, want := range []string{
		"-wrap-ttl=24h0m0s",
		`"celery-prod"`,
		"cidr_list=100.64.0.0/10,10.20.0.0/16",
		"token_bound_cidrs=100.64.0.0/10,10.20.0.0/16",
		"ttl=7776000",
		"num_uses=5",
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("full args missing %q: %s", want, full)
		}
	}
}

func TestOperatorAppRolePolicyScope(t *testing.T) {
	t.Parallel()
	p := operatorAppRolePolicyHCL
	for _, want := range []string{
		`path "auth/approle/role/tenant-+/secret-id" {`,
		`path "auth/approle/role/tenant-+/secret-id-accessor/destroy" {`,
		`path "auth/approle/role/tenant-+/role-id" {`,
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("policy missing %q:\n%s", want, p)
		}
	}
	// The operator token must not be able to touch KV, Transit, or policies.
	for _, forbidden := range []string{"kv/", "transit/", `path "sys/policies`, `"auth/approle/role/mksrv-operator`} {
		if strings.Contains(p, forbidden) {
			t.Fatalf("policy is too broad, contains %q:\n%s", forbidden, p)
		}
	}
}
