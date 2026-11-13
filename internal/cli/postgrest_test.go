// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os/exec"
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
	pt := true
	on := &model.TenantDatabase{PostgREST: &model.PostgRESTConfig{Enabled: &pt}}
	tenants := map[string]model.Tenant{
		"bitabit": {ID: "bitabit", Stacks: []string{"database"}, Database: on},
		"gtex":    {ID: "gtex", Stacks: []string{}, Database: on}, // no database stack
		"hg":      {ID: "hg", Stacks: []string{"database"}, Database: on},
		"mcps":    {ID: "mcps", Stacks: []string{"database"}}, // PostgREST is opt-in
		"nopgrst": {ID: "nopgrst", Stacks: []string{"database"}, Database: &model.TenantDatabase{PostgREST: &model.PostgRESTConfig{Enabled: &pf}}},
	}
	got := postgrestConsumerIDs(tenants)
	want := []string{"bitabit", "hg"}
	if len(got) != len(want) {
		t.Fatalf("postgrestConsumerIDs() = %v, want %v", got, want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("postgrestConsumerIDs() = %v, want %v", got, want)
		}
	}
}

func TestAssignPostgRESTPorts(t *testing.T) {
	t.Parallel()
	db := func(ids ...string) map[string]model.Tenant {
		m := map[string]model.Tenant{}
		for _, id := range ids {
			pt := true
			m[id] = model.Tenant{ID: id, Stacks: []string{"database"}, Database: &model.TenantDatabase{PostgREST: &model.PostgRESTConfig{Enabled: &pt}}}
		}
		return m
	}
	// No lock entry yet: reproduces the old alphabetical allocation.
	first, err := assignPostgRESTPorts(db("bitabit", "hg", "mcps"), nil)
	if err != nil || first["bitabit"] != 3010 || first["hg"] != 3011 || first["mcps"] != 3012 {
		t.Fatalf("seed = %v, %v", first, err)
	}
	// A tenant sorting first must not move the others, and takes the next free port.
	after, err := assignPostgRESTPorts(db("aaa", "bitabit", "hg", "mcps"), first)
	if err != nil || after["bitabit"] != 3010 || after["hg"] != 3011 || after["mcps"] != 3012 || after["aaa"] != 3013 {
		t.Fatalf("after adding aaa = %v, %v", after, err)
	}
	// A removed tenant frees its port for the next newcomer.
	reuse, _ := assignPostgRESTPorts(db("bitabit", "mcps", "zzz"), after)
	if reuse["bitabit"] != 3010 || reuse["mcps"] != 3012 || reuse["zzz"] != 3011 {
		t.Fatalf("reuse = %v", reuse)
	}
	// Non-consumers (gtex) never get a port.
	withWeb := db("bitabit")
	withWeb["gtex"] = model.Tenant{ID: "gtex"}
	if got, _ := assignPostgRESTPorts(withWeb, nil); len(got) != 1 {
		t.Fatalf("non-consumer assigned a port: %v", got)
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
	base := renderUnit(model.Tenant{ID: "bitabit"})
	for _, no := range []string{"PGRST_JWT_AUD", "PGRST_DB_MAX_ROWS", "PGRST_SERVER_CORS", "PGRST_OPENAPI_MODE"} {
		if strings.Contains(base, no) {
			t.Fatalf("unit without settings must not contain %s:\n%s", no, base)
		}
	}
	if !strings.Contains(base, "PGRST_DB_PRE_REQUEST=mksrv.pgrst_pre_request") || !strings.Contains(base, "PGRST_DB_SCHEMAS=app\n") {
		t.Fatalf("base unit changed:\n%s", base)
	}
	ext := renderUnit(model.Tenant{ID: "hg", Database: &model.TenantDatabase{Schema: "api", PostgREST: &model.PostgRESTConfig{
		Schemas: []string{"api", "reports"}, JWTAudience: "hg-lims-api", OpenAPIMode: "disabled", CORSOrigins: []string{"https://a.example.com", "https://b.example.com"}, MaxRows: 1000}}})
	for _, want := range []string{
		"PGRST_DB_SCHEMAS=api,reports", "PGRST_JWT_AUD=hg-lims-api", "PGRST_OPENAPI_MODE=disabled",
		"PGRST_SERVER_CORS_ALLOWED_ORIGINS=https://a.example.com,https://b.example.com",
		"PGRST_DB_MAX_ROWS=1000", "PGRST_DB_PRE_REQUEST=mksrv.pgrst_pre_request",
		"PGRST_APP_SETTINGS_OIDC_ISSUER=https://kc.example.com/realms/hg",
	} {
		if !strings.Contains(ext, want) {
			t.Fatalf("extended unit missing %q:\n%s", want, ext)
		}
	}
}

func TestJWKSRefreshListAndScript(t *testing.T) {
	t.Parallel()
	tenants := map[string]model.Tenant{
		"hg":   {ID: "hg", Keycloak: model.TenantKeycloak{Realm: "hg-realm"}},
		"mcps": {ID: "mcps"},
	}
	got := jwksRefreshList([]string{"mcps", "hg"}, tenants, "kc.example.com")
	want := "hg https://kc.example.com/realms/hg-realm/protocol/openid-connect/certs\n" +
		"mcps https://kc.example.com/realms/mcps/protocol/openid-connect/certs\n"
	if got != want {
		t.Fatalf("list = %q, want %q", got, want)
	}
	for _, need := range []string{"podman secret create --replace", `systemctl restart "mksrv-postgrest-$id.service"`, jwksRefreshListPath} {
		if !strings.Contains(jwksRefreshScript, need) {
			t.Fatalf("script missing %q", need)
		}
	}
	if !strings.Contains(jwksRefreshTimer, "OnUnitActiveSec=1h") || !strings.Contains(jwksRefreshService, jwksRefreshScriptPath) {
		t.Fatal("timer/service units malformed")
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(jwksRefreshScript)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("refresh script has a syntax error: %v\n%s", err, out)
		}
	}
}
