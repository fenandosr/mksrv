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
