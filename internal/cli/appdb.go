// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"strings"
)

// sqlQuote escapes a SQL string literal. The one shared implementation —
// previously copy-pasted once in tenantDatabaseSQL (database.go) and again in
// postgres.go's app-database bootstrap; a third copy here would be exactly
// the duplication ADR 0034 argues against.
func sqlQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// dedicatedAppDatabaseSQL provisions one standalone Postgres database + login
// role for a third-party app that manages its own schema and permissions end
// to end (Nextcloud, Superset, Keila, Plunk, Umami, …) — unlike
// tenantDatabaseSQL, there are no RBAC buckets and no PostgREST wiring here:
// the role owns its database outright, exactly as the app's own upstream
// instructions expect (ADR 0040). Idempotent; safe to re-run on an existing
// database (the role's password is reset every run, matching
// tenantDatabaseSQL's own ALTER ROLE idiom, so a leaked or rotated SSM value
// is always what's actually live).
func dedicatedAppDatabaseSQL(role, db, password string) string {
	q := sqlQuote
	return strings.Join([]string{
		fmt.Sprintf(`SELECT format('CREATE ROLE %%I LOGIN PASSWORD %%L', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\gexec`, q(role), q(password), q(role)),
		fmt.Sprintf(`ALTER ROLE %q WITH LOGIN PASSWORD %s;`, role, q(password)),
		// The owner stays the per-app role, not a global one: a global owner
		// could ALTER DATABASE any other app's/tenant's database from one
		// session, the same reasoning tenantDatabaseSQL already applies to
		// db_<id>.
		fmt.Sprintf(`SELECT format('CREATE DATABASE %%I OWNER %%I', %s, %s) WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = %s)\gexec`, q(db), q(role), q(db)),
		fmt.Sprintf(`REVOKE ALL ON DATABASE %q FROM PUBLIC;`, db),
		fmt.Sprintf(`GRANT ALL PRIVILEGES ON DATABASE %q TO %q;`, db, role),
		"",
	}, "\n")
}
