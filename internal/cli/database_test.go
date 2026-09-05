// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/infra"
	"github.com/fenandosr/mksrv/internal/model"
)

func TestPostgrestDSN(t *testing.T) {
	t.Parallel()
	standalone := postgrestDSN(postgresCluster{}, "acme", "p w")
	if standalone != "postgres://acme_auth:p w@mksrv-postgres:5432/db_acme" {
		t.Fatalf("standalone DSN = %q", standalone)
	}
	cluster := postgrestDSN(postgresCluster{Nodes: []postgresNode{
		{IP: "10.20.0.11"}, {IP: "10.20.0.12"}, {IP: "10.20.0.13"},
	}}, "acme", "pw")
	for _, want := range []string{
		"@10.20.0.11:5432,10.20.0.12:5432,10.20.0.13:5432/db_acme",
		"?target_session_attrs=read-write",
	} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster DSN missing %q: %s", want, cluster)
		}
	}
}

func TestPgConnSelectsClusterOrStandalone(t *testing.T) {
	t.Parallel()
	// cluster bootstrapped
	f := &fleet{
		postgres: postgresCluster{Primary: "core1"},
		byName:   map[string]hostTarget{"core1": {Name: "core1"}},
		outputs:  infra.Outputs{Hosts: map[string]infra.HostOutput{"core1": {PrivateIP: "10.20.0.11"}}},
	}
	pg, ok, err := f.pgConn()
	if err != nil || !ok || pg.container != "mksrv-patroni" || pg.superRef != "/mksrv/{env}/postgres/superpass" || pg.pgAdmin != "10.20.0.11" {
		t.Fatalf("cluster pgConn = %+v ok=%v err=%v", pg, ok, err)
	}

	// legacy postgres.json recorded the primary as an IP -> resolve via Nodes
	fIP := &fleet{
		postgres: postgresCluster{Primary: "10.20.0.11", Nodes: []postgresNode{{Host: "core1", IP: "10.20.0.11"}}},
		byName:   map[string]hostTarget{"core1": {Name: "core1"}},
		outputs:  infra.Outputs{Hosts: map[string]infra.HostOutput{"core1": {PrivateIP: "10.20.0.11"}}},
	}
	if pg, ok, err := fIP.pgConn(); err != nil || !ok || pg.name != "core1" {
		t.Fatalf("IP-primary pgConn = %+v ok=%v err=%v", pg, ok, err)
	}

	// postgres assigned but not bootstrapped -> error
	f2 := &fleet{targets: []hostTarget{{Name: "core1", Host: model.Host{Stacks: []string{"postgres"}}}}}
	if _, _, err := f2.pgConn(); err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatalf("want bootstrap error, got %v", err)
	}

	// standalone
	f3 := &fleet{targets: []hostTarget{{Name: "data", Host: model.Host{Stacks: []string{"database"}}}}}
	pg3, ok, err := f3.pgConn()
	if err != nil || !ok || pg3.container != "mksrv-postgres" {
		t.Fatalf("standalone pgConn = %+v ok=%v err=%v", pg3, ok, err)
	}
}

func TestPatroniLeaderIP(t *testing.T) {
	t.Parallel()
	rows := []patroniMember{
		{Member: "core1", Host: "10.20.0.21", Role: "Replica"},
		{Member: "core2", Host: "10.20.0.22", Role: "Leader"},
		{Member: "core3", Host: "10.20.0.23", Role: "Replica"},
	}
	if got := patroniLeaderIP(rows); got != "10.20.0.22" {
		t.Fatalf("patroniLeaderIP() = %q", got)
	}
	if got := patroniLeaderIP(nil); got != "" {
		t.Fatalf("patroniLeaderIP(nil) = %q, want empty", got)
	}
	if got := patroniLeaderIP([]patroniMember{{Member: "core1", Host: "10.20.0.21", Role: "Replica"}}); got != "" {
		t.Fatalf("patroniLeaderIP(no leader) = %q, want empty", got)
	}
}

func TestHostNameForPrivateIP(t *testing.T) {
	t.Parallel()
	outputs := infra.Outputs{Hosts: map[string]infra.HostOutput{
		"core1": {PrivateIP: "10.20.0.21"},
		"core2": {PrivateIP: "10.20.0.22"},
	}}
	if got := hostNameForPrivateIP(outputs, "10.20.0.22"); got != "core2" {
		t.Fatalf("hostNameForPrivateIP() = %q", got)
	}
	if got := hostNameForPrivateIP(outputs, "10.20.0.99"); got != "" {
		t.Fatalf("hostNameForPrivateIP(unknown) = %q, want empty", got)
	}
}

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
