// SPDX-License-Identifier: Apache-2.0

package render

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/schema"
)

func baseContext() Context {
	return Context{
		Env:       "prod",
		Timezone:  "America/Mexico_City",
		ACMEEmail: "ops@example.com",
		Host: HostView{
			Name:     "edge",
			Role:     "edge",
			PublicIP: "203.0.113.10",
			Stacks:   []string{"base", "identity"},
		},
		Endpoints: Endpoints{
			Keycloak:   "auth.example.com",
			Headscale:  "vpn.example.com",
			ConfigD:    "cfg.example.com",
			RootDomain: "example.com",
		},
		Images: map[string]string{"caddy": "docker.io/library/caddy:2.8"},
	}
}

func TestStackRendersBase(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	base := catalog["base"]
	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), base, baseContext())
	if err != nil {
		t.Fatalf("Stack() error = %v", err)
	}

	caddyfile, ok := files["/var/lib/mksrv/stacks/base/Caddyfile"]
	if !ok {
		t.Fatalf("no Caddyfile rendered; got %v", SortedPaths(files))
	}
	if !strings.Contains(string(caddyfile), "email ops@example.com") {
		t.Fatalf("Caddyfile missing ACME email:\n%s", caddyfile)
	}
	if !strings.Contains(string(caddyfile), "respond /healthz") {
		t.Fatalf("Caddyfile missing health endpoint:\n%s", caddyfile)
	}

	unit, ok := files["/etc/containers/systemd/mksrv-caddy.container"]
	if !ok {
		t.Fatal("no caddy .container rendered")
	}
	if !strings.Contains(string(unit), "Image=docker.io/library/caddy:2.8") {
		t.Fatalf("caddy unit missing pinned image:\n%s", unit)
	}
}

func TestStackRendersIdentity(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog["identity"], baseContext())
	if err != nil {
		t.Fatalf("Stack() error = %v", err)
	}
	frag, ok := files["/var/lib/mksrv/caddy.d/10-identity.caddy"]
	if !ok {
		t.Fatalf("no identity caddy fragment; got %v", SortedPaths(files))
	}
	if !strings.Contains(string(frag), "auth.example.com") || !strings.Contains(string(frag), "vpn.example.com") {
		t.Fatalf("fragment missing endpoints:\n%s", frag)
	}
	cfg, ok := files["/var/lib/mksrv/stacks/identity/headscale/config.yaml"]
	if !ok {
		t.Fatal("no headscale config rendered")
	}
	if !strings.Contains(string(cfg), "server_url: https://vpn.example.com") {
		t.Fatalf("headscale config wrong:\n%s", cfg)
	}
}

func TestStackRendersDataPlane(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	ctx := baseContext()
	ctx.Host.Name = "data"
	ctx.Host.Role = "data"
	ctx.Host.PrivateIP = "10.20.0.168"
	ctx.Host.TailnetIP = "100.64.0.1"
	ctx.Host.Stacks = []string{"database", "monitor"}
	ctx.Images = map[string]string{"postgres": "docker.io/library/postgres:16"}

	stacksRoot := filepath.Clean(filepath.Join("..", "..", "stacks"))
	dbFiles, err := Stack(stacksRoot, catalog["database"], ctx)
	if err != nil {
		t.Fatalf("Stack(database) error = %v", err)
	}
	if pg := string(dbFiles["/etc/containers/systemd/mksrv-postgres.container"]); !strings.Contains(pg, "PublishPort=100.64.0.1:5432:5432") {
		t.Fatalf("postgres unit missing tailnet publish:\n%s", pg)
	}

	monFiles, err := Stack(stacksRoot, catalog["monitor"], ctx)
	if err != nil {
		t.Fatalf("Stack(monitor) error = %v", err)
	}
	if frag := string(monFiles["/var/lib/mksrv/caddy.d/30-monitor.caddy"]); !strings.Contains(frag, "reverse_proxy 10.20.0.168:3000") {
		t.Fatalf("monitor fragment wrong:\n%s", frag)
	}

	// The per-tenant PostgREST unit is skipped when Tenant is nil...
	if _, ok := dbFiles["/etc/containers/systemd/mksrv-postgrest-{tenant}.container"]; ok {
		t.Fatal("postgrest unit rendered without a tenant")
	}
	// ...and named + wired per tenant when Tenant is set.
	ctx.Tenant = &model.Tenant{ID: "bitabit"}
	ctx.Secrets = map[string]string{"listen_port": "3010", "rest_fqdn": "bitabit.rest.example.com"}
	tFiles, err := Stack(stacksRoot, catalog["database"], ctx)
	if err != nil {
		t.Fatalf("Stack(database, tenant) error = %v", err)
	}
	pgrst := string(tFiles["/etc/containers/systemd/mksrv-postgrest-bitabit.container"])
	for _, want := range []string{
		"ContainerName=mksrv-postgrest-bitabit",
		"PublishPort=10.20.0.168:3010:3000",
		"PublishPort=100.64.0.1:3010:3000",
		"PGRST_DB_ANON_ROLE=bitabit_anon",
		"mksrv-database-postgrest-bitabit-jwt,type=env,target=PGRST_JWT_SECRET",
	} {
		if !strings.Contains(pgrst, want) {
			t.Fatalf("postgrest unit missing %q:\n%s", want, pgrst)
		}
	}

	ctx.Tenant = nil
	ctx.Secrets = map[string]string{"redis_admin_pass": "adminpw"}
	cacheFiles, err := Stack(stacksRoot, catalog["cache"], ctx)
	if err != nil {
		t.Fatalf("Stack(cache) error = %v", err)
	}
	if acl := string(cacheFiles["/var/lib/mksrv/stacks/cache/users.acl"]); !strings.Contains(acl, "user mksrv on >adminpw ~* &* +@all") {
		t.Fatalf("cache users.acl seed wrong:\n%s", acl)
	}
	if unit := string(cacheFiles["/etc/containers/systemd/mksrv-redis.container"]); !strings.Contains(unit, "PublishPort=100.64.0.1:6379:6379") {
		t.Fatalf("redis unit missing tailnet publish:\n%s", unit)
	}
}

func TestStackRendersLogsAndSecurity(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	stacksRoot := filepath.Clean(filepath.Join("..", "..", "stacks"))

	ctx := baseContext()
	ctx.Host.Name = "data"
	ctx.Host.Role = "data"
	ctx.Host.PrivateIP = "10.20.0.168"
	ctx.Host.Stacks = []string{"monitor", "logs"}
	ctx.StackHosts = map[string]string{"logs": "10.20.0.168", "security": "10.20.0.113"}

	logFiles, err := Stack(stacksRoot, catalog["logs"], ctx)
	if err != nil {
		t.Fatalf("Stack(logs) error = %v", err)
	}
	if u := string(logFiles["/etc/containers/systemd/mksrv-loki.container"]); !strings.Contains(u, "PublishPort=10.20.0.168:3100:3100") {
		t.Fatalf("loki unit wrong:\n%s", u)
	}
	if a := string(logFiles["/var/lib/mksrv/stacks/logs/config.alloy"]); !strings.Contains(a, `url = "http://10.20.0.168:3100/loki/api/v1/push"`) {
		t.Fatalf("alloy config wrong:\n%s", a)
	}

	// prometheus.yml and the grafana datasource pick up the extra scrape/source.
	monFiles, err := Stack(stacksRoot, catalog["monitor"], ctx)
	if err != nil {
		t.Fatalf("Stack(monitor) error = %v", err)
	}
	if p := string(monFiles["/var/lib/mksrv/stacks/monitor/prometheus.yml"]); !strings.Contains(p, "job_name: loki") || !strings.Contains(p, "10.20.0.113:6060") {
		t.Fatalf("prometheus.yml missing logs/security jobs:\n%s", p)
	}
	if d := string(monFiles["/var/lib/mksrv/stacks/monitor/grafana-datasource.yml"]); !strings.Contains(d, "type: loki") {
		t.Fatalf("grafana datasource missing Loki:\n%s", d)
	}

	// security stack on the edge.
	ectx := baseContext()
	ectx.Host.Name = "edge"
	ectx.Host.PrivateIP = "10.20.0.113"
	ectx.Host.Stacks = []string{"base", "security"}
	ectx.Secrets = map[string]string{"bouncer_key": "s3cr3t-key"}
	secFiles, err := Stack(stacksRoot, catalog["security"], ectx)
	if err != nil {
		t.Fatalf("Stack(security) error = %v", err)
	}
	if b := string(secFiles["/etc/containers/systemd/mksrv-crowdsec-fw.container"]); !strings.Contains(b, "Network=host") || !strings.Contains(b, "AddCapability=NET_ADMIN") {
		t.Fatalf("firewall bouncer unit wrong:\n%s", b)
	}
	if y := string(secFiles["/var/lib/mksrv/stacks/security/bouncer.yaml"]); !strings.Contains(y, "api_key: s3cr3t-key") {
		t.Fatalf("bouncer.yaml missing key:\n%s", y)
	}
	if c := string(secFiles["/etc/containers/systemd/mksrv-crowdsec.container"]); !strings.Contains(c, "target=BOUNCER_KEY_firewall") {
		t.Fatalf("crowdsec unit missing bouncer key secret:\n%s", c)
	}
}

func TestStackIP(t *testing.T) {
	t.Parallel()
	ctx := baseContext()
	ctx.StackHosts = map[string]string{"monitor": "10.0.0.5"}
	if ctx.StackIP("monitor") != "10.0.0.5" || ctx.StackIP("logs") != "" {
		t.Fatalf("StackIP wrong: %q %q", ctx.StackIP("monitor"), ctx.StackIP("logs"))
	}
}

func TestMonitorOmitsLogsJobWhenAbsent(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	ctx := baseContext()
	ctx.Host.Name = "data"
	ctx.Host.PrivateIP = "10.20.0.168"
	ctx.Host.Stacks = []string{"monitor"}
	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog["monitor"], ctx)
	if err != nil {
		t.Fatalf("Stack(monitor) error = %v", err)
	}
	if p := string(files["/var/lib/mksrv/stacks/monitor/prometheus.yml"]); strings.Contains(p, "job_name: loki") {
		t.Fatalf("prometheus.yml has a loki job with no logs stack:\n%s", p)
	}
}

func TestContextHelpers(t *testing.T) {
	t.Parallel()
	ctx := baseContext()
	if !ctx.HasStack("identity") || ctx.HasStack("mail") {
		t.Fatal("HasStack wrong")
	}
	if ctx.Image("caddy") == "" || ctx.Image("missing") != "" {
		t.Fatal("Image wrong")
	}
}
