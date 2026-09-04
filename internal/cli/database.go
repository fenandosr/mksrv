// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

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
	defer client.Close()

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
		sql := tenantDatabaseSQL(id, dbPass, authPass)
		cmd := fmt.Sprintf(
			"sudo podman exec -e PGPASSWORD=%s -i %s psql -v ON_ERROR_STOP=1 -U %s -d postgres",
			quoteArg(superPass), pg.container, pg.psqlUser,
		)
		if _, err := client.RunInput(ctx, cmd, []byte(sql)); err != nil {
			return fmt.Errorf("provision database for %s: %w", id, err)
		}
		pgTenants = append(pgTenants, id)
		printer.Success("tenant %s: database db_%s and role %s ready", id, id, id)
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
			"Username":      id,
			"SSLMode":       "prefer",
			"Shared":        true,
		}
	}
	blob, err := json.Marshal(map[string]any{"Servers": servers})
	if err != nil {
		return err
	}
	if _, err := client.RunInput(ctx,
		"sudo podman exec -i mksrv-pgadmin sh -c 'cat > /tmp/mksrv-servers.json' 2>/dev/null || sudo tee /tmp/mksrv-servers.json >/dev/null && sudo podman cp /tmp/mksrv-servers.json mksrv-pgadmin:/tmp/mksrv-servers.json",
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

func tenantDatabaseSQL(id, password, authPassword string) string {
	db := "db_" + id
	role := id           // dev / admin: owns schema app, full DML + DDL
	app := id + "_app"   // apps group: SELECT by default, dev grants writes per table
	anon := id + "_anon" // token-less
	web := id + "_web"   // PostgREST impersonation landing role
	auth := id + "_auth" // authenticator (connection role)
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	create := func(name, opts string) string {
		return fmt.Sprintf(`SELECT format('CREATE ROLE %%I %s', %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, opts, q(name), q(name))
	}
	return strings.Join([]string{
		fmt.Sprintf(`SELECT format('CREATE ROLE %%I LOGIN PASSWORD %%L', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, q(role), q(password), q(role)),
		fmt.Sprintf(`ALTER ROLE %q WITH LOGIN PASSWORD %s;`, role, q(password)),
		fmt.Sprintf(`SELECT format('CREATE DATABASE %%I OWNER %%I', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = %s)\gexec`, q(db), q(role), q(db)),
		fmt.Sprintf(`REVOKE ALL ON DATABASE %q FROM PUBLIC;`, db),
		fmt.Sprintf(`GRANT CONNECT, CREATE ON DATABASE %q TO %q;`, db, role),
		// PostgREST role graph (ADR 0016). auth logs in; a db-pre-request
		// function switches from web to role / app / anon by the token's groups.
		create(anon, "NOLOGIN"),
		create(app, "NOLOGIN"),
		create(web, "NOLOGIN NOINHERIT"),
		fmt.Sprintf(`SELECT format('CREATE ROLE %%I LOGIN NOINHERIT PASSWORD %%L', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, q(auth), q(authPassword), q(auth)),
		fmt.Sprintf(`ALTER ROLE %q WITH LOGIN NOINHERIT PASSWORD %s;`, auth, q(authPassword)),
		fmt.Sprintf(`GRANT %q, %q, %q TO %q;`, role, app, anon, web),
		fmt.Sprintf(`GRANT %q TO %q;`, web, auth),
		fmt.Sprintf(`GRANT CONNECT ON DATABASE %q TO %q;`, db, auth),
		fmt.Sprintf(`\connect %q`, db),
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS app AUTHORIZATION %q;`, role),
		fmt.Sprintf(`ALTER DATABASE %q SET search_path TO app, public;`, db),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %q IN SCHEMA app GRANT ALL ON TABLES TO %q;`, role, role),
		fmt.Sprintf(`GRANT USAGE ON SCHEMA app TO %q, %q, %q;`, anon, app, web),
		fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA app TO %q, %q;`, anon, app),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %q IN SCHEMA app GRANT SELECT ON TABLES TO %q, %q;`, role, anon, app),
		// db-pre-request: token-less requests early-return (PostgREST already
		// set anon); otherwise SET LOCAL ROLE by the `groups` claim.
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION app.pgrst_pre_request() RETURNS void LANGUAGE plpgsql AS $mksrv$
DECLARE claims text := current_setting('request.jwt.claims', true); grps jsonb;
BEGIN
  IF claims IS NULL OR claims = '' THEN RETURN; END IF;
  grps := coalesce((claims::jsonb) -> 'groups', '[]'::jsonb);
  IF grps ? 'admin' OR grps ? 'dev' THEN SET LOCAL ROLE %q;
  ELSIF grps ? 'apps' THEN SET LOCAL ROLE %q;
  ELSE SET LOCAL ROLE %q;
  END IF;
END;
$mksrv$;`, role, app, anon),
		fmt.Sprintf(`ALTER FUNCTION app.pgrst_pre_request() OWNER TO %q;`, role),
		fmt.Sprintf(`GRANT EXECUTE ON FUNCTION app.pgrst_pre_request() TO %q, %q;`, web, anon),
		"",
	}, "\n")
}
