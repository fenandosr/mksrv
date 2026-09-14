// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/model"
)

func TestMailPasswordRef(t *testing.T) {
	t.Parallel()
	if got := mailPasswordRef("mcps", "gen@acme.example.com"); got != "/mksrv/{env}/mail/tenant_mcps_gen_password" {
		t.Fatalf("ref = %q", got)
	}
	if got := mailPasswordRef("mcps", "alberto.zarza@acme.example.com"); got != "/mksrv/{env}/mail/tenant_mcps_alberto_zarza_password" {
		t.Fatalf("ref with dot = %q", got)
	}
}

// TestMailPasswordHashRef guards the migrated-mailbox path: an operator
// seeds this ref directly with a pre-computed `{SHA512-CRYPT}$6$...` hash
// (from another mail server's own postfix-accounts.cf) so the user's
// existing password keeps working. Same local-part sanitizing as
// mailPasswordRef, distinct suffix, and distinct from it — reconcileMailboxes
// checks this one first and never writes to it.
func TestMailPasswordHashRef(t *testing.T) {
	t.Parallel()
	if got := mailPasswordHashRef("mcps", "alberto.zarza@acme.example.com"); got != "/mksrv/{env}/mail/tenant_mcps_alberto_zarza_password_hash" {
		t.Fatalf("ref = %q", got)
	}
	if mailPasswordHashRef("mcps", "gen@acme.example.com") == mailPasswordRef("mcps", "gen@acme.example.com") {
		t.Fatal("password and password-hash refs must not collide")
	}
}

func TestMailTenants(t *testing.T) {
	t.Parallel()
	tenants := map[string]model.Tenant{
		"mcps":   {Mail: &model.TenantMail{Domains: []string{"acme.example.com"}, Hosted: true}},
		"hg":     {Mail: &model.TenantMail{}}, // no domains -> not a mail consumer
		"acme":   {},
		"future": {Mail: &model.TenantMail{Domains: []string{"future.example.com"}}}, // hosted:false -> not live yet
	}
	got := mailTenants(tenants)
	if strings.Join(got, ",") != "mcps" {
		t.Fatalf("mailTenants = %v, want [mcps]", got)
	}
}

// TestTenantMailHosted guards a real regression: provisionMail's DKIM loop
// used to gate on `t.Mail != nil` alone (matching a tenant with a
// documentation-only `mail:` block — SPF/DMARC intent recorded ahead of an
// actual migration, `hosted` unset) and tried to generate DKIM for it too,
// erroring against a mail server that tenant never opted into. Every
// mail-provisioning path must gate on this one predicate instead.
func TestTenantMailHosted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		t    model.Tenant
		want bool
	}{
		{"hosted with domain", model.Tenant{Mail: &model.TenantMail{Domains: []string{"acme.example.com"}, Hosted: true}}, true},
		{"documentation only, no hosted", model.Tenant{Mail: &model.TenantMail{Domains: []string{"acme.example.com"}, DMARCRUA: "dmarc@acme.example.com"}}, false},
		{"hosted but no domains", model.Tenant{Mail: &model.TenantMail{Hosted: true}}, false},
		{"no mail block", model.Tenant{}, false},
	}
	for _, c := range cases {
		if got := tenantMailHosted(c.t); got != c.want {
			t.Errorf("%s: tenantMailHosted() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseDKIMRecord(t *testing.T) {
	t.Parallel()
	raw := `mail._domainkey	IN	TXT	( "v=DKIM1; h=sha256; k=rsa; "
	  "p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA1234ABCD" )  ; ----- DKIM key mail for acme.example.com`
	got := parseDKIMRecord(raw)
	want := "v=DKIM1; h=sha256; k=rsa; p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA1234ABCD"
	if got != want {
		t.Fatalf("parseDKIMRecord =\n%q\nwant\n%q", got, want)
	}
	if got := parseDKIMRecord("nothing quoted here"); got != "" {
		t.Fatalf("no quotes should parse empty, got %q", got)
	}
	if got := parseDKIMRecord(`"not dkim"`); got != "" {
		t.Fatalf("non-DKIM quoted content should parse empty, got %q", got)
	}
}

func TestMailHostAndSelection(t *testing.T) {
	t.Parallel()
	f := &fleet{targets: []hostTarget{
		{Name: "edge", Host: model.Host{Stacks: []string{"base", "identity", "mail"}}},
		{Name: "appd", Host: model.Host{Stacks: []string{"database"}}},
	}}
	h := f.mailHost()
	if h == nil || h.Name != "edge" {
		t.Fatalf("mailHost() = %+v", h)
	}

	f2 := &fleet{targets: []hostTarget{{Name: "edge", Host: model.Host{Stacks: []string{"base"}}}}}
	if h := f2.mailHost(); h != nil {
		t.Fatalf("mailHost() without the stack = %+v, want nil", h)
	}
}

func TestMTASTSFragmentPath(t *testing.T) {
	t.Parallel()
	if got := mtaSTSFragmentPath("mcps", "mcps-epcm.org"); got != "/var/lib/mksrv/caddy.d/16-mta-sts-mcps-mcps-epcm.org.caddy" {
		t.Fatalf("path = %q", got)
	}
}

// TestMTASTSPolicyAlwaysTesting guards the one hard rule in the doc comment
// on model.TenantMail.MTASTS: mksrv never publishes `mode: enforce` itself,
// regardless of what a tenant's DMARC/SPF posture already is elsewhere —
// there's no way to know a prior policy (on infrastructure mksrv never saw)
// was already in enforce, or to monitor delivery before flipping to it.
func TestMTASTSPolicyAlwaysTesting(t *testing.T) {
	t.Parallel()
	policy := mtaSTSPolicy("mail.cloud-it.click")
	for _, want := range []string{"version: STSv1", "mode: testing", "mx: mail.cloud-it.click", "max_age: 604800"} {
		if !strings.Contains(policy, want) {
			t.Fatalf("policy missing %q:\n%s", want, policy)
		}
	}
	if strings.Contains(policy, "enforce") {
		t.Fatalf("policy must never say enforce:\n%s", policy)
	}
}

// TestMTASTSFragmentServesWellKnownOnly guards that the site block answers
// only the exact well-known path and 404s everything else on that hostname —
// mta-sts.<domain> shouldn't silently fall through to the edge's default
// handling for an unrelated path.
func TestMTASTSFragmentServesWellKnownOnly(t *testing.T) {
	t.Parallel()
	frag := mtaSTSFragment("mcps-epcm.org", "mail.cloud-it.click")
	if !strings.HasPrefix(frag, "mta-sts.mcps-epcm.org {") {
		t.Fatalf("fragment missing the mta-sts hostname:\n%s", frag)
	}
	if !strings.Contains(frag, "/.well-known/mta-sts.txt") {
		t.Fatalf("fragment missing the well-known path:\n%s", frag)
	}
	if !strings.Contains(frag, "mode: testing") {
		t.Fatalf("fragment must embed the testing-mode policy body:\n%s", frag)
	}
	if !strings.Contains(frag, "respond 404") {
		t.Fatalf("fragment must 404 everything but the well-known path:\n%s", frag)
	}
}
