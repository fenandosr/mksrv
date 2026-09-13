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

	// Service: true is recorded in metadata for audit — which AppRole path it
	// actually mints against (tenant-<id> vs tenant-<id>-svc) is runTenantSecretID's
	// job, not secretIDMintArgs's.
	svc := strings.Join(secretIDMintArgs(role, tenantSecretIDOptions{Service: true, WrapTTL: time.Hour}), " ")
	if !strings.Contains(svc, `"tier":"svc"`) {
		t.Fatalf("service args missing tier=svc metadata: %s", svc)
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

// TestOperatorAppRolePolicyScope guards a real, live-confirmed OpenBao ACL
// limitation: this engine does not glob-match `+` or `*` embedded between
// literal text in a path segment (`tenant-+/secret-id` and `tenant-*/secret-id`
// were both denied a request an identical *literal* path granted) — so the
// operator's policy must spell out one explicit block per tenant id, not a
// wildcard, or minting silently 403s for every tenant.
func TestOperatorAppRolePolicyScope(t *testing.T) {
	t.Parallel()
	p := operatorAppRolePolicyHCL([]string{"acme", "hg"})
	for _, want := range []string{
		`path "auth/approle/role/tenant-acme/secret-id" {`,
		`path "auth/approle/role/tenant-acme/secret-id-accessor/destroy" {`,
		`path "auth/approle/role/tenant-acme/role-id" {`,
		`path "auth/approle/role/svc-acme/secret-id" {`,
		`path "auth/approle/role/svc-acme/secret-id-accessor/destroy" {`,
		`path "auth/approle/role/svc-acme/role-id" {`,
		// A second id in ids must get its own blocks too — this is the whole
		// point of generating the policy from the roster instead of hardcoding
		// one tenant, or a single-tenant workspace would mask the bug above.
		`path "auth/approle/role/tenant-hg/secret-id" {`,
		`path "auth/approle/role/svc-hg/secret-id" {`,
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("policy missing %q:\n%s", want, p)
		}
	}
	// No capability on a role's own definition path (just its sub-paths) —
	// the operator can mint/revoke credentials, never repoint the role at a
	// different policy.
	if strings.Contains(p, `path "auth/approle/role/tenant-acme" {`) ||
		strings.Contains(p, `path "auth/approle/role/svc-acme" {`) {
		t.Fatalf("policy grants access to a role's own definition path:\n%s", p)
	}
	// An id absent from the roster gets no blocks at all.
	if strings.Contains(p, "mcps") {
		t.Fatalf("policy leaked a block for an id not in ids:\n%s", p)
	}
	// The operator token must not be able to touch KV, Transit, or policies.
	for _, forbidden := range []string{"kv/", "transit/", `path "sys/policies`, `"auth/approle/role/mksrv-operator`} {
		if strings.Contains(p, forbidden) {
			t.Fatalf("policy is too broad, contains %q:\n%s", forbidden, p)
		}
	}
}
