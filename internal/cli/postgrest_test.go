// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/render"
)

// TestPostgrestConsumerIDsExcludesNonConsumers guards the live incident this
// was extracted to fix: postgrestPort used to index into every tenant in the
// workspace, sorted — so a `web:`-only tenant with no `database` stack (like
// `gtex`, alongside `bitabit`/`hg`/`mcps`) still occupied a slot and shifted
// every actual PostgREST consumer's port by one. A stale, not-yet-restarted
// container (mcps, still on its old port) then collided with the tenant whose
// port shifted onto it (hg), crash-looping hg's postgrest in production.
// Confirmed live and fixed by anchoring the index to consumers only.
func TestPostgrestConsumerIDsExcludesNonConsumers(t *testing.T) {
	t.Parallel()
	pf := false
	tenants := map[string]model.Tenant{
		"bitabit": {ID: "bitabit", Stacks: []string{"database"}},
		"gtex":    {ID: "gtex", Stacks: []string{}}, // web:-only, no database stack
		"hg":      {ID: "hg", Stacks: []string{"database"}},
		"mcps":    {ID: "mcps", Stacks: []string{"database"}},
		"nopgrst": {ID: "nopgrst", Stacks: []string{"database"}, Database: &model.TenantDatabase{PostgREST: &model.PostgRESTConfig{Enabled: &pf}}},
	}
	got := postgrestConsumerIDs(tenants)
	want := []string{"bitabit", "hg", "mcps"}
	if len(got) != len(want) {
		t.Fatalf("postgrestConsumerIDs() = %v, want %v", got, want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("postgrestConsumerIDs() = %v, want %v", got, want)
		}
	}
}

// TestPostgrestPortStableAcrossUnrelatedTenants guards that adding a
// non-consumer tenant (gtex) never shifts an existing consumer's (mcps) port
// — the live bug this was extracted to fix.
func TestPostgrestPortStableAcrossUnrelatedTenants(t *testing.T) {
	t.Parallel()
	before := postgrestConsumerIDs(map[string]model.Tenant{
		"bitabit": {ID: "bitabit", Stacks: []string{"database"}},
		"hg":      {ID: "hg", Stacks: []string{"database"}},
		"mcps":    {ID: "mcps", Stacks: []string{"database"}},
	})
	after := postgrestConsumerIDs(map[string]model.Tenant{
		"bitabit": {ID: "bitabit", Stacks: []string{"database"}},
		"gtex":    {ID: "gtex", Stacks: []string{}},
		"hg":      {ID: "hg", Stacks: []string{"database"}},
		"mcps":    {ID: "mcps", Stacks: []string{"database"}},
	})
	for _, id := range []string{"bitabit", "hg", "mcps"} {
		gotBefore := postgrestPort(before, id)
		gotAfter := postgrestPort(after, id)
		if gotBefore != gotAfter {
			t.Fatalf("tenant %s: port %d before gtex existed, %d after — adding an unrelated non-database tenant must not shift it", id, gotBefore, gotAfter)
		}
	}
}

func TestPostgrestPort(t *testing.T) {
	t.Parallel()
	ids := []string{"bitabit", "hg", "mcps"}
	for i, id := range ids {
		if got, want := postgrestPort(ids, id), postgrestBasePort+i; got != want {
			t.Fatalf("postgrestPort(%q) = %d, want %d", id, got, want)
		}
	}
	if got := postgrestPort(ids, "gtex"); got != 0 {
		t.Fatalf("postgrestPort() for a tenant not in the list = %d, want 0", got)
	}
}

func TestPostgrestTemplateExtendedEnv(t *testing.T) {
	t.Parallel()
	renderUnit := func(tenant model.Tenant) string {
		out, err := render.One("../../stacks/database/templates/postgrest.container.tmpl", render.Context{
			Tenant:  &tenant,
			Secrets: map[string]string{"listen_port": "3010", "rest_fqdn": "x.rest.example.com", "oidc_issuer": "https://kc.example.com/realms/hg"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	legacy := renderUnit(model.Tenant{ID: "bitabit"})
	for _, no := range []string{"PGRST_JWT_AUD", "PGRST_APP_SETTINGS", "PGRST_DB_MAX_ROWS", "mksrv.pgrst_pre_request"} {
		if strings.Contains(legacy, no) {
			t.Fatalf("legacy unit must not contain %s:\n%s", no, legacy)
		}
	}
	if !strings.Contains(legacy, "PGRST_DB_PRE_REQUEST=app.pgrst_pre_request") || !strings.Contains(legacy, "PGRST_DB_SCHEMAS=app\n") {
		t.Fatalf("legacy unit changed:\n%s", legacy)
	}
	ext := renderUnit(model.Tenant{ID: "hg", Database: &model.TenantDatabase{Schema: "api", PostgREST: &model.PostgRESTConfig{
		Schemas: []string{"api", "reports"}, JWTAudience: "hg-lims-api", CORSOrigins: []string{"https://a.example.com", "https://b.example.com"}, MaxRows: 1000}}})
	for _, want := range []string{
		"PGRST_DB_SCHEMAS=api,reports", "PGRST_JWT_AUD=hg-lims-api",
		"PGRST_SERVER_CORS_ALLOWED_ORIGINS=https://a.example.com,https://b.example.com",
		"PGRST_DB_MAX_ROWS=1000", "PGRST_DB_PRE_REQUEST=mksrv.pgrst_pre_request",
		"PGRST_APP_SETTINGS_OIDC_ISSUER=https://kc.example.com/realms/hg",
	} {
		if !strings.Contains(ext, want) {
			t.Fatalf("extended unit missing %q:\n%s", want, ext)
		}
	}
}
