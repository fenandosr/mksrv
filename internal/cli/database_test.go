// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"
)

func TestTenantDatabaseSQL(t *testing.T) {
	t.Parallel()
	sql := tenantDatabaseSQL("bitabit", "s3cr3t'value", "auth'pw")
	for _, want := range []string{
		`CREATE DATABASE %I OWNER %I`,
		`CREATE ROLE %I LOGIN PASSWORD %L`,
		`\connect "db_bitabit"`,
		`CREATE SCHEMA IF NOT EXISTS app AUTHORIZATION "bitabit"`,
		`'s3cr3t''value'`, // single quote doubled
		`CREATE ROLE %I LOGIN NOINHERIT PASSWORD %L`,
		`'auth''pw'`, // authenticator password quoted
		// RBAC role graph (M19)
		`'bitabit_app'`,
		`'bitabit_web'`,
		`GRANT "bitabit", "bitabit_app", "bitabit_anon" TO "bitabit_web"`,
		`GRANT "bitabit_web" TO "bitabit_auth"`,
		`CREATE OR REPLACE FUNCTION app.pgrst_pre_request()`,
		`IF grps ? 'admin' OR grps ? 'dev' THEN SET LOCAL ROLE "bitabit";`,
		`ELSIF grps ? 'apps' THEN SET LOCAL ROLE "bitabit_app";`,
		`GRANT EXECUTE ON FUNCTION app.pgrst_pre_request() TO "bitabit_web", "bitabit_anon"`,
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL missing %q:\n%s", want, sql)
		}
	}
}
