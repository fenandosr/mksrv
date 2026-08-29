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
		`CREATE ROLE %I NOLOGIN`,
		`CREATE ROLE %I LOGIN NOINHERIT PASSWORD %L`,
		`GRANT "bitabit" TO "bitabit_auth"`,
		`GRANT USAGE ON SCHEMA app TO "bitabit_anon"`,
		`'auth''pw'`, // authenticator password quoted
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL missing %q:\n%s", want, sql)
		}
	}
}
