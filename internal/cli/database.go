// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/fenandosr/mksrv/internal/infra"
	"github.com/fenandosr/mksrv/internal/model"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

// pgConn is where the tenant-provisioning SQL runs: the Patroni primary when a
// `postgres` cluster is in the fleet, otherwise the `database` stack's own
// standalone Postgres.
type pgConn struct {
	target    sshx.Target
	name      string
	container string
	psqlUser  string
	superRef  string
	pgAdmin   string // Host value pgAdmin registers (a node IP, or the container name)
}

func (f *fleet) pgConn() (pgConn, bool, error) {
	if f.postgres.Primary != "" {
		name := f.postgres.Primary
		if _, ok := f.byName[name]; !ok {
			// Older .mksrv/postgres.json recorded the primary as an IP.
			for _, n := range f.postgres.Nodes {
				if n.IP == f.postgres.Primary && n.Host != "" {
					name = n.Host
				}
			}
		}
		ht, ok := f.byName[name]
		if !ok {
			return pgConn{}, false, fmt.Errorf("postgres primary %q is not a fleet host (re-run `mksrv postgres bootstrap`)", f.postgres.Primary)
		}
		ip := f.outputs.Hosts[ht.Name].PrivateIP
		return pgConn{ht.Target, ht.Name, "mksrv-patroni", "postgres", "/mksrv/{env}/postgres/superpass", ip}, true, nil
	}
	for i := range f.targets {
		if slices.Contains(f.targets[i].Host.Stacks, "postgres") {
			return pgConn{}, false, fmt.Errorf("a host carries `postgres` but the cluster is not bootstrapped — run `mksrv postgres bootstrap` first")
		}
	}
	for i := range f.targets {
		if slices.Contains(f.targets[i].Host.Stacks, "database") {
			return pgConn{f.targets[i].Target, f.targets[i].Name, "mksrv-postgres", "mksrv", "/mksrv/{env}/database/pg_superpass", "mksrv-postgres"}, true, nil
		}
	}
	return pgConn{}, false, nil
}

// ensurePrimary re-checks that pg actually points at the current Patroni
// leader before DDL runs against it. .mksrv/postgres.json's recorded primary
// is a snapshot from the last `mksrv postgres bootstrap` — any failover since
// (a rolling restart from a later `mksrv apply` touching the postgres stack
// included) leaves it stale, and running DDL against a now-demoted replica
// fails with "cannot execute ALTER ROLE in a read-only transaction". Patroni
// answers `patronictl list` from any node regardless of role, so this check
// works even when pg currently points at a replica. Best-effort: on any
// problem checking, it proceeds with the pg/client it was given rather than
// failing the caller outright. Closes the old client and self-heals
// .mksrv/postgres.json when it redials a different (real) leader.
func (f *fleet) ensurePrimary(ctx context.Context, client *sshx.Client, pg pgConn) (pgConn, *sshx.Client) {
	if f.postgres.Primary == "" {
		return pg, client // standalone: pg.container is the only postgres there is
	}
	res, err := client.Run(ctx, "sudo podman exec "+pg.container+" patronictl -c /etc/patroni/patroni.yml list -f json")
	if err != nil {
		return pg, client
	}
	var rows []patroniMember
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		return pg, client
	}
	leaderIP := patroniLeaderIP(rows)
	if leaderIP == "" || leaderIP == pg.pgAdmin {
		return pg, client // already the leader, or the cluster didn't report one
	}
	leaderName := hostNameForPrivateIP(f.outputs, leaderIP)
	ht, ok := f.byName[leaderName]
	if leaderName == "" || !ok {
		return pg, client
	}
	newClient, err := sshx.Dial(ctx, ht.Target, f.knownHosts)
	if err != nil {
		return pg, client
	}
	client.Close()
	pg.target, pg.name, pg.pgAdmin = ht.Target, leaderName, leaderIP
	f.postgres.Primary = leaderName
	_ = f.writePostgres(f.postgres)
	return pg, newClient
}

// patroniLeaderIP returns the private IP of the `patronictl list` row with
// Role "leader" (case-insensitive), or "" if none is reported.
func patroniLeaderIP(rows []patroniMember) string {
	for _, r := range rows {
		if strings.EqualFold(r.Role, "leader") {
			return r.Host
		}
	}
	return ""
}

// hostNameForPrivateIP returns the fleet host name whose private IP matches
// ip, or "" if none does.
func hostNameForPrivateIP(outputs infra.Outputs, ip string) string {
	for name, out := range outputs.Hosts {
		if out.PrivateIP == ip {
			return name
		}
	}
	return ""
}

// provisionDatabases creates one database, login role, and app schema per
// tenant that consumes the `database` stack. It is idempotent and only runs
// when a data host carries `database`.
func (f *fleet) provisionDatabases(ctx context.Context, printer ui.Printer, tenants []string) error {
	var dataHost *hostTarget
	for i := range f.targets {
		if slices.Contains(f.targets[i].Host.Stacks, "database") {
			dataHost = &f.targets[i]
			break
		}
	}
	if dataHost == nil {
		return nil
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}
	pg, ok, err := f.pgConn()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	superPass, err := f.resolver.Get(ctx, pg.superRef)
	if err != nil {
		return fmt.Errorf("postgres superuser password: %w (deploy postgres first)", err)
	}

	client, err := sshx.Dial(ctx, pg.target, f.knownHosts)
	if err != nil {
		return dialError(pg.name, err)
	}
	pg, client = f.ensurePrimary(ctx, client, pg)
	defer client.Close()

	psql := func(db, sql string) error {
		cmd := fmt.Sprintf(
			"sudo podman exec -e PGPASSWORD=%s -i %s psql -v ON_ERROR_STOP=1 -U %s -d %s",
			quoteArg(superPass), pg.container, pg.psqlUser, db,
		)
		_, e := client.RunInput(ctx, cmd, []byte(sql))
		return e
	}

	// The RBAC privilege buckets are cluster-global (ADR 0026): create them
	// once, not per tenant.
	if err := psql("postgres", globalRBACRolesSQL()); err != nil {
		return fmt.Errorf("ensure global RBAC roles: %w", err)
	}

	var pgTenants []string
	for _, id := range tenants {
		if !slices.Contains(f.data.Tenants[id].Stacks, "database") {
			continue
		}
		dbPass, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/database/tenant_"+id+"_password", 24)
		if err != nil {
			return err
		}
		authPass := ""
		if f.data.Tenants[id].PostgRESTEnabled() {
			if authPass, err = f.resolver.EnsureRandom(ctx, "/mksrv/{env}/database/tenant_"+id+"_authpw", 24); err != nil {
				return err
			}
		}
		svcPass := map[string]string{}
		for role := range f.data.Tenants[id].DBServiceRoles() {
			pw, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/database/tenant_"+id+"_svc_"+role+"_password", 24)
			if err != nil {
				return err
			}
			svcPass[role] = pw
		}
		if err := psql("postgres", tenantDatabaseSQLWith(id, dbPass, authPass, svcPass, f.data.Tenants[id])); err != nil {
			return fmt.Errorf("provision database for %s: %w", id, err)
		}
		pgTenants = append(pgTenants, id)
		printer.Success("tenant %s: database db_%s and role %s_login ready", id, id, id)
	}

	if len(pgTenants) > 0 {
		adminClient := client
		if dataHost.Name != pg.name {
			adminClient, err = sshx.Dial(ctx, dataHost.Target, f.knownHosts)
			if err != nil {
				return dialError(dataHost.Name, err)
			}
			defer adminClient.Close()
		}
		if err := loadPgAdminServers(ctx, adminClient, f.data.Deployment.Identity.ACMEEmail, pg.pgAdmin, pgTenants); err != nil {
			printer.Warn("pgAdmin server list not loaded: %v", err)
		} else {
			printer.Info("pgAdmin servers registered for %d tenants", len(pgTenants))
		}
	}
	return nil
}

// loadPgAdminServers pre-registers the tenant databases in pgAdmin so operators
// only need to enter the password.
func loadPgAdminServers(ctx context.Context, client *sshx.Client, pgadminUser, pgHost string, tenants []string) error {
	servers := map[string]any{}
	for i, id := range tenants {
		servers[fmt.Sprintf("%d", i+1)] = map[string]any{
			"Name":          fmt.Sprintf("%s (db_%s)", id, id),
			"Group":         "mksrv tenants",
			"Host":          pgHost,
			"Port":          5432,
			"MaintenanceDB": "db_" + id,
			"Username":      id + "_login",
			"SSLMode":       "prefer",
			"Shared":        true,
		}
	}
	blob, err := json.Marshal(map[string]any{"Servers": servers})
	if err != nil {
		return err
	}
	if _, err := client.RunInput(ctx,
		"sudo tee /tmp/mksrv-servers.json >/dev/null && sudo podman cp /tmp/mksrv-servers.json mksrv-pgadmin:/tmp/mksrv-servers.json",
		blob,
	); err != nil {
		return err
	}
	_, err = client.Run(ctx, fmt.Sprintf(
		"sudo podman exec mksrv-pgadmin /venv/bin/python3 /pgadmin4/setup.py load-servers /tmp/mksrv-servers.json --user %s",
		quoteArg(pgadminUser),
	))
	return err
}

// RBAC roles (ADR 0026). The privilege buckets are cluster-global — their job
// is identical for every tenant and PostgreSQL object privileges are already
// per-database, so a bucket used in a db_<id> session only ever sees that
// tenant's data. mksrv sets no role attributes on them.
const (
	dbOwnerRole = "mksrv_owner" // owns schema app + its objects in every tenant DB; the admin/dev DDL identity
	dbAppRole   = "mksrv_app"   // apps group: SELECT on app by default
	dbAnonRole  = "mksrv_anon"  // token-less
	dbWebRole   = "mksrv_web"   // PostgREST impersonation landing role
)

// globalRBACRolesSQL creates the four cluster-global buckets and their
// membership graph. Idempotent; run once per `mksrv tenant apply`, before any
// tenant database. Fixed role names — no user input, so a plain \gexec guard is
// enough.
func globalRBACRolesSQL() string {
	create := func(name, opts string) string {
		return fmt.Sprintf(`SELECT 'CREATE ROLE %s %s' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s')\gexec`, name, opts, name)
	}
	return strings.Join([]string{
		create(dbOwnerRole, "NOLOGIN"),
		create(dbAppRole, "NOLOGIN"),
		create(dbAnonRole, "NOLOGIN"),
		create(dbWebRole, "NOLOGIN NOINHERIT"),
		fmt.Sprintf(`GRANT %s, %s, %s TO %s;`, dbOwnerRole, dbAppRole, dbAnonRole, dbWebRole),
		// mksrv_owner also needs membership in the lesser buckets: the dev
		// guide documents `SET ROLE mksrv_app` for request handlers to drop
		// from mksrv_owner's full DDL to the app's normal, granted-only
		// privilege set (docs/tenant-dev-guide.md, "Least privilege for
		// request handling") — without this grant, every <id>_login session
		// (which runs as mksrv_owner, see tenantDatabaseSQL) gets "permission
		// denied to set role" the moment it tries. Found while answering an
		// integrating tenant that wanted exactly this migrations/runtime
		// role split.
		fmt.Sprintf(`GRANT %s, %s TO %s;`, dbAppRole, dbAnonRole, dbOwnerRole),
		"",
	}, "\n")
}

// tenantDatabaseSQL provisions one tenant: the two per-tenant LOGIN roles (their
// password + the db CONNECT grant are the isolation boundary), the database, any
// requested extensions, and the application schema wired to the global buckets
// (ADR 0026 + ADR 0029). Idempotent.
func tenantDatabaseSQL(id, password, authPassword string, t model.Tenant) string {
	return tenantDatabaseSQLWith(id, password, authPassword, nil, t)
}

// tenantDatabaseSQLWith is tenantDatabaseSQL plus the generated passwords of the
// tenant's declared service roles (ADR 0039 §3), keyed by role name.
func tenantDatabaseSQLWith(id, password, authPassword string, servicePasswords map[string]string, t model.Tenant) string {
	db := "db_" + id
	login := id + "_login" // humans (admin/dev) over the VPN; member of mksrv_owner
	schema := t.DBSchema()
	q := sqlQuote

	connLimit := "-1"
	if cl := t.DBConnectionLimit(); cl > 0 {
		connLimit = strconv.Itoa(cl)
	}

	stmts := []string{
		fmt.Sprintf(`SELECT format('CREATE ROLE %%I LOGIN PASSWORD %%L', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, q(login), q(password), q(login)),
		fmt.Sprintf(`ALTER ROLE %q WITH LOGIN PASSWORD %s CONNECTION LIMIT %s;`, login, q(password), connLimit),
		fmt.Sprintf(`GRANT %s TO %q;`, dbOwnerRole, login),
		// Objects are owned by whoever runs CREATE, not an inherited role. This
		// makes every <id>_login session start as mksrv_owner, so DDL — direct
		// or via PostgREST's SET LOCAL ROLE mksrv_owner — always produces
		// mksrv_owner-owned objects and one set of default privileges applies.
		// session_user stays <id>_login (logs / pg_stat_activity keep the
		// tenant); only current_user reads mksrv_owner.
		fmt.Sprintf(`ALTER ROLE %q SET role TO %s;`, login, dbOwnerRole),

		// db_<id> owner stays the per-tenant role: a global database owner could
		// ALTER DATABASE db_<other> from any session.
		fmt.Sprintf(`SELECT format('CREATE DATABASE %%I OWNER %%I', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = %s)\gexec`, q(db), q(login), q(db)),
		fmt.Sprintf(`REVOKE ALL ON DATABASE %q FROM PUBLIC;`, db),
		fmt.Sprintf(`GRANT CONNECT, CREATE ON DATABASE %q TO %q;`, db, login),
	}
	stmts = append(stmts, authenticatorSQL(id, authPassword, t)...)
	stmts = append(stmts, tenantRolesSQL(id, t, servicePasswords)...)
	stmts = append(stmts, fmt.Sprintf(`\connect %q`, db))

	for _, ext := range t.DBExtensions() {
		stmts = append(stmts, fmt.Sprintf(`CREATE EXTENSION IF NOT EXISTS %q;`, ext))
	}

	// public_schema_create (opt-in, default false — ADR 0026 leaves `public`
	// closed): some apps (confirmed with Vikunja) hardcode object creation
	// in `public` regardless of search_path. Every <id>_login session runs
	// as mksrv_owner (ALTER ROLE ... SET role TO, above) — that's the role
	// whose privileges actually get checked, granting the tenant's own
	// login role directly here would do nothing. Scoped to this db_<id>
	// only: GRANT ON SCHEMA is per-database, so this never reaches another
	// tenant's db_<other>.public.
	if t.Database != nil && t.Database.PublicSchemaCreate {
		stmts = append(stmts, fmt.Sprintf(`GRANT CREATE ON SCHEMA public TO %s;`, dbOwnerRole))
	}

	stmts = append(stmts,
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q AUTHORIZATION %s;`, schema, dbOwnerRole),
		fmt.Sprintf(`ALTER SCHEMA %q OWNER TO %s;`, schema, dbOwnerRole),
	)
	stmts = append(stmts, privateSchemasSQL(t)...)
	stmts = append(stmts,
		fmt.Sprintf(`ALTER DATABASE %q SET search_path TO %q, public;`, db, schema),
		fmt.Sprintf(`GRANT USAGE ON SCHEMA %q TO %s, %s, %s;`, schema, dbAppRole, dbAnonRole, dbWebRole),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %q GRANT ALL ON TABLES TO %s;`, dbOwnerRole, schema, dbOwnerRole),
		// mksrv_anon (the PostgREST no-token role) gets NO blanket SELECT
		// (ADR 0029) — PostgREST's URL is public, so a tenant grants anon
		// access per table for exactly what it wants public. mksrv_app is
		// authenticated + group-gated, so it keeps the default.
		appSelectSQL(t, schema)[0],
		appSelectSQL(t, schema)[1],
		// Heal a database provisioned before ADR 0029 (the old SQL blanket-
		// granted anon).
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %q REVOKE SELECT ON TABLES FROM %s;`, dbOwnerRole, schema, dbAnonRole),
		fmt.Sprintf(`REVOKE SELECT ON ALL TABLES IN SCHEMA %q FROM %s;`, schema, dbAnonRole),
	)
	stmts = append(stmts, postgrestSQL(t)...)
	stmts = append(stmts, "")
	return strings.Join(stmts, "\n")
}

// privateSchemasSQL creates database.schemas: extra schemas owned by
// mksrv_owner with no USAGE for the shared buckets, so they are neither exposed
// by PostgREST nor reachable by mksrv_app / mksrv_anon / mksrv_web.
func privateSchemasSQL(t model.Tenant) []string {
	if t.Database == nil {
		return nil
	}
	var out []string
	for _, s := range t.Database.Schemas {
		out = append(out,
			fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q AUTHORIZATION %s;`, s, dbOwnerRole),
			fmt.Sprintf(`ALTER SCHEMA %q OWNER TO %s;`, s, dbOwnerRole),
		)
	}
	return out
}

// authenticatorSQL creates the PostgREST authenticator (<id>_auth) for tenants
// that opted in, and removes it (with its CONNECT grant) for tenants that did
// not, so turning PostgREST off leaves no role behind. Runs before \connect.
func authenticatorSQL(id, authPassword string, t model.Tenant) []string {
	db, auth := "db_"+id, id+"_auth"
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	if !t.PostgRESTEnabled() {
		return []string{
			// DROP ROLE fails while the role still holds privileges; the only
			// ones mksrv ever grants it are CONNECT and the mksrv_web membership.
			fmt.Sprintf(`SELECT format('REVOKE ALL ON DATABASE %%I FROM %%I', %s, %s) WHERE EXISTS (SELECT FROM pg_roles WHERE rolname = %s) AND EXISTS (SELECT FROM pg_database WHERE datname = %s)\gexec`, q(db), q(auth), q(auth), q(db)),
			fmt.Sprintf(`SELECT format('DROP ROLE %%I', %s) WHERE EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, q(auth), q(auth)),
		}
	}
	return []string{
		fmt.Sprintf(`SELECT format('CREATE ROLE %%I LOGIN NOINHERIT PASSWORD %%L', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, q(auth), q(authPassword), q(auth)),
		fmt.Sprintf(`ALTER ROLE %q WITH LOGIN NOINHERIT PASSWORD %s;`, auth, q(authPassword)),
		fmt.Sprintf(`GRANT %s TO %q;`, dbWebRole, auth),
		fmt.Sprintf(`GRANT CONNECT ON DATABASE %q TO %q;`, db, auth),
	}
}

// postgrestSQL is the in-database half of PostgREST (ADR 0039): extra exposed
// schemas and the generated pre-request in the non-exposed `mksrv` schema (so it
// is neither an RPC nor in the OpenAPI document). Tenants without PostgREST get
// the function dropped instead.
func postgrestSQL(t model.Tenant) []string {
	schema := t.DBSchema()
	if !t.PostgRESTEnabled() {
		return []string{
			`DROP FUNCTION IF EXISTS mksrv.pgrst_pre_request();`,
			`DROP SCHEMA IF EXISTS mksrv;`,
			fmt.Sprintf(`DROP FUNCTION IF EXISTS %q.pgrst_pre_request();`, schema),
		}
	}
	out := []string{}
	for _, s := range t.PostgRESTSchemas() {
		if s == schema {
			continue
		}
		out = append(out,
			fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q AUTHORIZATION %s;`, s, dbOwnerRole),
			fmt.Sprintf(`ALTER SCHEMA %q OWNER TO %s;`, s, dbOwnerRole),
			fmt.Sprintf(`GRANT USAGE ON SCHEMA %q TO %s, %s, %s;`, s, dbAppRole, dbAnonRole, dbWebRole),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %q GRANT ALL ON TABLES TO %s;`, dbOwnerRole, s, dbOwnerRole),
		)
	}
	out = append(out,
		// Heal tenants provisioned before ADR 0039: the old function lived in
		// the exposed application schema (and so showed up as an RPC).
		fmt.Sprintf(`DROP FUNCTION IF EXISTS %q.pgrst_pre_request();`, schema),
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS mksrv AUTHORIZATION %s;`, dbOwnerRole),
		fmt.Sprintf(`GRANT USAGE ON SCHEMA mksrv TO %s, %s;`, dbWebRole, dbAnonRole),
		generatedPreRequestSQL(t),
		fmt.Sprintf(`ALTER FUNCTION mksrv.pgrst_pre_request() OWNER TO %s;`, dbOwnerRole),
		fmt.Sprintf(`GRANT EXECUTE ON FUNCTION mksrv.pgrst_pre_request() TO %s, %s;`, dbWebRole, dbAnonRole),
	)
	return out
}

// roleRule is one `when` mapping, ordered most-specific first.
type roleRule struct {
	role   string
	groups []string
}

// inheritedRoles returns every declared role reachable from name through
// member_of, excluding name itself (cycles are rejected by validation, but the
// walk is guarded anyway).
func inheritedRoles(roles map[string]model.DBRole, name string) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		for _, m := range roles[n].MemberOf {
			if m != name && !seen[m] {
				seen[m] = true
				walk(m)
			}
		}
	}
	walk(name)
	return seen
}

// roleRules returns the tenant's `when` mappings. YAML maps carry no order
// (the loader goes through JSON), so rules are ordered deterministically: more
// required groups first, then by role name. First match wins.
func roleRules(t model.Tenant) []roleRule {
	if t.Database == nil {
		return nil
	}
	var rules []roleRule
	for name, r := range t.Database.Roles {
		if len(r.When) > 0 {
			g := append([]string(nil), r.When...)
			sort.Strings(g)
			rules = append(rules, roleRule{role: name, groups: g})
		}
	}
	// A role that inherits another (member_of, transitively) must be tried first:
	// a user holding the groups of both should land in the broader role, not in
	// the one it already includes. Then more required groups, then name.
	inherited := make(map[string]int, len(rules))
	for _, r := range rules {
		inherited[r.role] = len(inheritedRoles(t.Database.Roles, r.role))
	}
	sort.Slice(rules, func(i, j int) bool {
		if a, b := inherited[rules[i].role], inherited[rules[j].role]; a != b {
			return a > b
		}
		if len(rules[i].groups) != len(rules[j].groups) {
			return len(rules[i].groups) > len(rules[j].groups)
		}
		return rules[i].role < rules[j].role
	})
	return rules
}

// generatedPreRequestSQL builds mksrv.pgrst_pre_request() (ADR 0039 §2). With
// at least one `when` rule the admin/dev -> mksrv_owner shortcut is gone and
// unmatched tokens are denied (42501 -> HTTP 403) unless rbac.unmapped is
// "anon". Without rules the legacy mapping is kept. The optional hook runs
// after SET LOCAL ROLE, i.e. under the business role.
func generatedPreRequestSQL(t model.Tenant) string {
	lit := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	rules := roleRules(t)
	var b strings.Builder
	b.WriteString("CREATE OR REPLACE FUNCTION mksrv.pgrst_pre_request() RETURNS void LANGUAGE plpgsql AS $mksrv$\n")
	b.WriteString("DECLARE claims text := current_setting('request.jwt.claims', true); grps jsonb;\nBEGIN\n")
	// Token-less requests reach here too: PostgREST already set the anon role and
	// fills request.jwt.claims with just {"role": "<anon>"}. Only a real token
	// (Keycloak always sets `sub`) is mapped — and denied when unmapped.
	b.WriteString("  IF claims IS NULL OR claims = '' OR NOT ((claims::jsonb) ? 'sub') THEN RETURN; END IF;\n")
	b.WriteString("  grps := coalesce((claims::jsonb) -> 'groups', '[]'::jsonb);\n")
	if len(rules) == 0 {
		b.WriteString("  IF grps ? 'admin' OR grps ? 'dev' THEN SET LOCAL ROLE mksrv_owner;\n")
		b.WriteString("  ELSIF grps ? 'apps' THEN SET LOCAL ROLE mksrv_app;\n")
		b.WriteString("  ELSE SET LOCAL ROLE mksrv_anon;\n  END IF;\n")
	} else {
		for i, r := range rules {
			kw := "ELSIF"
			if i == 0 {
				kw = "IF"
			}
			lits := make([]string, len(r.groups))
			for k, g := range r.groups {
				lits[k] = lit(g)
			}
			fmt.Fprintf(&b, "  %s grps ?& ARRAY[%s] THEN SET LOCAL ROLE %q;\n", kw, strings.Join(lits, ", "), r.role)
		}
		if t.Database.RBAC != nil && t.Database.RBAC.Unmapped == "anon" {
			b.WriteString("  ELSE SET LOCAL ROLE mksrv_anon;\n")
		} else {
			b.WriteString("  ELSE RAISE EXCEPTION 'no role mapped for token groups' USING ERRCODE = '42501';\n")
		}
		b.WriteString("  END IF;\n")
	}
	if hook := t.PostgRESTSettings().PreRequestHook; hook != "" {
		sch, fn, _ := strings.Cut(hook, ".")
		fmt.Fprintf(&b, "  PERFORM %q.%q();\n", sch, fn)
	}
	b.WriteString("END;\n$mksrv$;")
	return b.String()
}

// appSelectSQL returns the two statements that control mksrv_app's SELECT on
// the tenant schema: default grant (ADR 0026/0029), or with
// default_privileges: none the explicit revoke (ADR 0039 §3).
func appSelectSQL(t model.Tenant, schema string) [2]string {
	if t.Database != nil && t.Database.DefaultPrivileges == "none" {
		return [2]string{
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %q REVOKE SELECT ON TABLES FROM %s;`, dbOwnerRole, schema, dbAppRole),
			fmt.Sprintf(`REVOKE SELECT ON ALL TABLES IN SCHEMA %q FROM %s;`, schema, dbAppRole),
		}
	}
	return [2]string{
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %q GRANT SELECT ON TABLES TO %s;`, dbOwnerRole, schema, dbAppRole),
		fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA %q TO %s;`, schema, dbAppRole),
	}
}

// tenantRolesSQL creates the tenant-declared roles (ADR 0039 §3). Roles are
// cluster-global, so creation is idempotent and attributes are pinned; the
// tenant's authenticator <id>_auth (never mksrv_web) is the only role granted
// membership in the NOLOGIN roles, so it can assume only its own tenant's
// roles. Service roles are LOGIN, get CONNECT on db_<id> only, and are not
// reachable through the authenticator. Object privileges are the app's
// migrations' job.
func tenantRolesSQL(id string, t model.Tenant, servicePasswords map[string]string) []string {
	if t.Database == nil || (len(t.Database.Roles) == 0 && len(t.Database.ServiceRoles) == 0) {
		return nil
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	const safe = "NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"
	auth, db := id+"_auth", "db_"+id

	names := make([]string, 0, len(t.Database.Roles))
	for n := range t.Database.Roles {
		names = append(names, n)
	}
	sort.Strings(names)
	svc := make([]string, 0, len(t.Database.ServiceRoles))
	for n := range t.Database.ServiceRoles {
		svc = append(svc, n)
	}
	sort.Strings(svc)

	var out []string
	for _, n := range names {
		out = append(out,
			fmt.Sprintf(`SELECT format('CREATE ROLE %%I NOLOGIN %s', %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, safe, q(n), q(n)),
			fmt.Sprintf(`ALTER ROLE %q NOLOGIN %s;`, n, safe),
		)
	}
	for _, n := range svc {
		limit := "-1"
		if cl := t.Database.ServiceRoles[n].ConnectionLimit; cl > 0 {
			limit = strconv.Itoa(cl)
		}
		out = append(out,
			fmt.Sprintf(`SELECT format('CREATE ROLE %%I LOGIN %s PASSWORD %%L', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, safe, q(n), q(servicePasswords[n]), q(n)),
			fmt.Sprintf(`ALTER ROLE %q WITH LOGIN %s PASSWORD %s CONNECTION LIMIT %s;`, n, safe, q(servicePasswords[n]), limit),
			fmt.Sprintf(`GRANT CONNECT ON DATABASE %q TO %q;`, db, n),
		)
	}
	for _, n := range names {
		for _, m := range t.Database.Roles[n].MemberOf {
			out = append(out, fmt.Sprintf(`GRANT %q TO %q;`, m, n))
		}
		if t.PostgRESTEnabled() {
			out = append(out, fmt.Sprintf(`GRANT %q TO %q;`, n, auth))
		}
	}
	for _, n := range svc {
		for _, m := range t.Database.ServiceRoles[n].MemberOf {
			out = append(out, fmt.Sprintf(`GRANT %q TO %q;`, m, n))
		}
	}
	// Membership is declarative among the tenant's own declared roles: a
	// membership between two of them that the YAML no longer lists is revoked.
	// Only real memberships produce a REVOKE (the query reads pg_auth_members),
	// and roles outside the declared set, or <id>_auth's grants, are never touched.
	pairs := []string{"('', '')"}
	for _, n := range names {
		for _, m := range t.Database.Roles[n].MemberOf {
			pairs = append(pairs, fmt.Sprintf("(%s, %s)", q(n), q(m)))
		}
	}
	for _, n := range svc {
		for _, m := range t.Database.ServiceRoles[n].MemberOf {
			pairs = append(pairs, fmt.Sprintf("(%s, %s)", q(n), q(m)))
		}
	}
	quoted := func(list []string) string {
		qs := make([]string, len(list))
		for i, n := range list {
			qs[i] = q(n)
		}
		return strings.Join(qs, ", ")
	}
	out = append(out, fmt.Sprintf(`SELECT format('REVOKE %%I FROM %%I', g.rolname, m.rolname) FROM pg_auth_members am JOIN pg_roles g ON g.oid = am.roleid JOIN pg_roles m ON m.oid = am.member WHERE g.rolname IN (%s) AND m.rolname IN (%s) AND (m.rolname, g.rolname) NOT IN (VALUES %s)\gexec`,
		quoted(names), quoted(append(append([]string(nil), names...), svc...)), strings.Join(pairs, ", ")))
	return out
}
