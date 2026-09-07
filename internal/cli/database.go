// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/fenandosr/mksrv/internal/infra"
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
		authPass, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/database/tenant_"+id+"_authpw", 24)
		if err != nil {
			return err
		}
		if err := psql("postgres", tenantDatabaseSQL(id, dbPass, authPass)); err != nil {
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

// pgrstPreRequestSQL is now tenant-independent — it targets the global buckets.
// Token-less requests early-return (PostgREST already set the anon role);
// otherwise SET LOCAL ROLE by the `groups` claim.
const pgrstPreRequestSQL = `CREATE OR REPLACE FUNCTION app.pgrst_pre_request() RETURNS void LANGUAGE plpgsql AS $mksrv$
DECLARE claims text := current_setting('request.jwt.claims', true); grps jsonb;
BEGIN
  IF claims IS NULL OR claims = '' THEN RETURN; END IF;
  grps := coalesce((claims::jsonb) -> 'groups', '[]'::jsonb);
  IF grps ? 'admin' OR grps ? 'dev' THEN SET LOCAL ROLE mksrv_owner;
  ELSIF grps ? 'apps' THEN SET LOCAL ROLE mksrv_app;
  ELSE SET LOCAL ROLE mksrv_anon;
  END IF;
END;
$mksrv$;`

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
		"",
	}, "\n")
}

// tenantDatabaseSQL provisions one tenant: the two per-tenant LOGIN roles (their
// password + the db CONNECT grant are the isolation boundary), the database,
// and the `app` schema wired to the global buckets (ADR 0026). Idempotent.
func tenantDatabaseSQL(id, password, authPassword string) string {
	db := "db_" + id
	login := id + "_login" // humans (admin/dev) over the VPN; member of mksrv_owner
	auth := id + "_auth"   // PostgREST authenticator; member of mksrv_web
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	return strings.Join([]string{
		fmt.Sprintf(`SELECT format('CREATE ROLE %%I LOGIN PASSWORD %%L', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, q(login), q(password), q(login)),
		fmt.Sprintf(`ALTER ROLE %q WITH LOGIN PASSWORD %s;`, login, q(password)),
		fmt.Sprintf(`GRANT %s TO %q;`, dbOwnerRole, login),
		// Objects are owned by whoever runs CREATE, not an inherited role. This
		// makes every <id>_login session start as mksrv_owner, so DDL — direct
		// or via PostgREST's SET LOCAL ROLE mksrv_owner — always produces
		// mksrv_owner-owned objects and one set of default privileges applies.
		// session_user stays <id>_login (logs / pg_stat_activity keep the
		// tenant); only current_user reads mksrv_owner.
		fmt.Sprintf(`ALTER ROLE %q SET role TO %s;`, login, dbOwnerRole),

		fmt.Sprintf(`SELECT format('CREATE ROLE %%I LOGIN NOINHERIT PASSWORD %%L', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, q(auth), q(authPassword), q(auth)),
		fmt.Sprintf(`ALTER ROLE %q WITH LOGIN NOINHERIT PASSWORD %s;`, auth, q(authPassword)),
		fmt.Sprintf(`GRANT %s TO %q;`, dbWebRole, auth),

		// db_<id> owner stays the per-tenant role: a global database owner could
		// ALTER DATABASE db_<other> from any session.
		fmt.Sprintf(`SELECT format('CREATE DATABASE %%I OWNER %%I', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = %s)\gexec`, q(db), q(login), q(db)),
		fmt.Sprintf(`REVOKE ALL ON DATABASE %q FROM PUBLIC;`, db),
		fmt.Sprintf(`GRANT CONNECT, CREATE ON DATABASE %q TO %q;`, db, login),
		fmt.Sprintf(`GRANT CONNECT ON DATABASE %q TO %q;`, db, auth),

		fmt.Sprintf(`\connect %q`, db),
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS app AUTHORIZATION %s;`, dbOwnerRole),
		fmt.Sprintf(`ALTER SCHEMA app OWNER TO %s;`, dbOwnerRole),
		fmt.Sprintf(`ALTER DATABASE %q SET search_path TO app, public;`, db),
		fmt.Sprintf(`GRANT USAGE ON SCHEMA app TO %s, %s, %s;`, dbAppRole, dbAnonRole, dbWebRole),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA app GRANT ALL ON TABLES TO %s;`, dbOwnerRole, dbOwnerRole),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA app GRANT SELECT ON TABLES TO %s, %s;`, dbOwnerRole, dbAppRole, dbAnonRole),
		fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA app TO %s, %s;`, dbAppRole, dbAnonRole),

		pgrstPreRequestSQL,
		fmt.Sprintf(`ALTER FUNCTION app.pgrst_pre_request() OWNER TO %s;`, dbOwnerRole),
		fmt.Sprintf(`GRANT EXECUTE ON FUNCTION app.pgrst_pre_request() TO %s, %s;`, dbWebRole, dbAnonRole),
		"",
	}, "\n")
}
