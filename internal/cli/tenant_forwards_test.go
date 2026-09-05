// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"net"
	"regexp"
	"testing"

	"github.com/fenandosr/mksrv/internal/configd"
	"github.com/fenandosr/mksrv/internal/model"
)

func TestTenantForwardsTranslation(t *testing.T) {
	t.Parallel()
	tenant := model.Tenant{Forwards: []model.TenantForward{
		{ID: "login", Label: "Login node", Type: "ssh", Target: "mcps-login.prod.mksrv:22", SSHAlias: "mcps-login"},
		{ID: "jupyter", Label: "JupyterHub", Type: "http", Target: "mcps-login.prod.mksrv:8000", Open: "browser"},
		{ID: "db", Label: "Cluster DB", Type: "tcp", Target: "mcps-login.prod.mksrv:5432"},
	}}
	got := tenantForwards(tenant)
	if len(got) != 3 {
		t.Fatalf("want 3 forwards, got %d", len(got))
	}

	ssh := got[0]
	if ssh.SSH == nil || ssh.SSH.ConfigAlias != "mcps-login" || ssh.SSH.HostKeyAlias != "mcps-login.prod.mksrv" || !ssh.SSH.WriteConfig {
		t.Fatalf("ssh forward wrong: %+v / %+v", ssh, ssh.SSH)
	}
	if ssh.OpenAction.Kind != "ssh-terminal" || ssh.HealthCheck.Kind != "tcp" {
		t.Fatalf("ssh forward defaults wrong: %+v", ssh)
	}
	if got[1].OpenAction.Kind != "browser" || got[1].HealthCheck.Kind != "http" || got[1].HealthCheck.Path != "/" {
		t.Fatalf("http forward defaults wrong: %+v", got[1])
	}
	if got[2].OpenAction.Kind != "none" {
		t.Fatalf("tcp forward default open wrong: %+v", got[2])
	}

	// The combined roster set must satisfy Cloud-IT VPN's forward rules.
	fwdTargets := builtinForwardTargets{
		Edge:     "prod-edge.prod.mksrv",
		Postgres: "prod-core2.prod.mksrv",
		Rest:     "prod-appd.prod.mksrv",
		Cache:    "prod-appd.prod.mksrv",
		OpenBao:  "prod-core1.prod.mksrv",
	}
	all := append(builtinForwards(fwdTargets, 3010, 6379, true), got...)
	assertForwardsValid(t, all)
}

func TestDemoForwardsTargetsAndGating(t *testing.T) {
	t.Parallel()
	fwdTargets := builtinForwardTargets{
		Edge:     "prod-edge.prod.mksrv",
		Postgres: "prod-core2.prod.mksrv",
		Rest:     "prod-appd.prod.mksrv",
		Cache:    "prod-appd.prod.mksrv",
		OpenBao:  "prod-core1.prod.mksrv",
	}
	byID := func(fs []configd.Forward) map[string]string {
		m := map[string]string{}
		for _, f := range fs {
			m[f.ID] = f.Target
		}
		return m
	}
	full := byID(builtinForwards(fwdTargets, 3010, 6379, true))
	if full["edge-health"] != "prod-edge.prod.mksrv:80" ||
		full["database"] != "prod-core2.prod.mksrv:5432" ||
		full["rest"] != "prod-appd.prod.mksrv:3010" ||
		full["cache"] != "prod-appd.prod.mksrv:6379" ||
		full["openbao"] != "prod-core1.prod.mksrv:8200" {
		t.Fatalf("targets wrong: %+v", full)
	}
	// A tenant that consumes none of database/cache/openbao gets just the two
	// always-on forwards.
	lean := byID(builtinForwards(fwdTargets, 0, 0, false))
	if len(lean) != 2 || lean["rest"] != "" || lean["cache"] != "" || lean["openbao"] != "" {
		t.Fatalf("gating wrong: %+v", lean)
	}
	// openbao gated on the flag even when the target is known.
	if byID(builtinForwards(fwdTargets, 0, 0, true))["openbao"] != "prod-core1.prod.mksrv:8200" {
		t.Fatalf("openbao forward should render when the flag is set")
	}
	// An empty target drops its forward entirely.
	none := builtinForwards(builtinForwardTargets{}, 3010, 6379, true)
	if len(none) != 0 {
		t.Fatalf("empty targets should yield no forwards, got %+v", none)
	}
}

var forwardIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,31}$`)

// assertForwardsValid mirrors the load-bearing checks in cloud-it-vpn's
// internal/config.validateForward so a roster change that would be rejected by
// the desktop client fails here instead.
func assertForwardsValid(t *testing.T, forwards []configd.Forward) {
	t.Helper()
	if len(forwards) < 1 || len(forwards) > 32 {
		t.Fatalf("forward count %d outside 1-32", len(forwards))
	}
	seen := map[string]bool{}
	for _, f := range forwards {
		if !forwardIDRe.MatchString(f.ID) {
			t.Fatalf("forward id %q invalid", f.ID)
		}
		if seen[f.ID] {
			t.Fatalf("duplicate forward id %q", f.ID)
		}
		seen[f.ID] = true
		if f.Type != "http" && f.Type != "tcp" && f.Type != "ssh" {
			t.Fatalf("forward %s bad type %q", f.ID, f.Type)
		}
		if f.Listen.Host != "127.0.0.1" {
			t.Fatalf("forward %s listen host %q", f.ID, f.Listen.Host)
		}
		if f.PortStrategy != "auto" && f.PortStrategy != "fixed" {
			t.Fatalf("forward %s port strategy %q", f.ID, f.PortStrategy)
		}
		if _, _, err := net.SplitHostPort(f.Target); err != nil {
			t.Fatalf("forward %s target %q not host:port", f.ID, f.Target)
		}
		switch f.OpenAction.Kind {
		case "browser", "ssh-terminal", "copy", "none":
		default:
			t.Fatalf("forward %s open kind %q", f.ID, f.OpenAction.Kind)
		}
		if f.HealthCheck.Kind != "http" && f.HealthCheck.Kind != "tcp" {
			t.Fatalf("forward %s health kind %q", f.ID, f.HealthCheck.Kind)
		}
		if f.HealthCheck.IntervalSec < 5 || f.HealthCheck.IntervalSec > 3600 {
			t.Fatalf("forward %s health interval %d", f.ID, f.HealthCheck.IntervalSec)
		}
		if f.MaxConns < 1 || f.MaxConns > 1024 {
			t.Fatalf("forward %s maxConns %d", f.ID, f.MaxConns)
		}
		if f.Type == "ssh" && (f.SSH == nil || f.SSH.ConfigAlias == "" || f.SSH.KnownHostsFile == "" || f.SSH.HostKeyAlias == "") {
			t.Fatalf("forward %s ssh settings incomplete: %+v", f.ID, f.SSH)
		}
	}
}
