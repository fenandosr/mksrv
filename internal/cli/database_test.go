// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"
)

func TestTenantDatabaseSQL(t *testing.T) {
	t.Parallel()
	sql := tenantDatabaseSQL("bitabit", "s3cr3t'value")
	for _, want := range []string{
		`CREATE DATABASE %I OWNER %I`,
		`CREATE ROLE %I LOGIN PASSWORD %L`,
		`\connect "db_bitabit"`,
		`CREATE SCHEMA IF NOT EXISTS app AUTHORIZATION "bitabit"`,
		`'s3cr3t''value'`, // single quote doubled
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL missing %q:\n%s", want, sql)
		}
	}
}
