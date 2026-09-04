// SPDX-License-Identifier: Apache-2.0

package render

import (
	"encoding/json"
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
		Images:    map[string]string{"caddy": "docker.io/library/caddy:2.8"},
		Retention: (&model.RetentionConfig{}).Resolved(),
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
	if pa := string(dbFiles["/etc/containers/systemd/mksrv-pgadmin.container"]); !strings.Contains(pa, "Requires=mksrv-postgres.service") {
		t.Fatalf("standalone pgadmin should require the local postgres unit:\n%s", pa)
	}

	// With a `postgres` cluster in the fleet, the standalone unit renders empty
	// and consumers must not reference the (nonexistent) local postgres unit.
	clusterCtx := ctx
	clusterCtx.StackHosts = map[string]string{"postgres": "10.20.0.11"}
	cFiles, err := Stack(stacksRoot, catalog["database"], clusterCtx)
	if err != nil {
		t.Fatalf("Stack(database, cluster) error = %v", err)
	}
	if pg := strings.TrimSpace(string(cFiles["/etc/containers/systemd/mksrv-postgres.container"])); pg != "" {
		t.Fatalf("standalone postgres unit should be empty when a cluster exists:\n%s", pg)
	}
	if pa := string(cFiles["/etc/containers/systemd/mksrv-pgadmin.container"]); strings.Contains(pa, "mksrv-postgres.service") {
		t.Fatalf("cluster-mode pgadmin must not reference the local postgres unit:\n%s", pa)
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
		"PGRST_DB_PRE_REQUEST=app.pgrst_pre_request",
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
	} else if strings.Contains(acl, "#") {
		// Redis's aclfile parser rejects comment lines and aborts startup.
		t.Fatalf("cache users.acl must not contain comments:\n%s", acl)
	}
	if unit := string(cacheFiles["/etc/containers/systemd/mksrv-redis.container"]); !strings.Contains(unit, "PublishPort=100.64.0.1:6379:6379") {
		t.Fatalf("redis unit missing tailnet publish:\n%s", unit)
	}
	if unit := string(cacheFiles["/etc/containers/systemd/mksrv-redis-exporter.container"]); !strings.Contains(unit, "Network=host") ||
		!strings.Contains(unit, "REDIS_ADDR=redis://127.0.0.1:6379") || !strings.Contains(unit, "target=REDIS_PASSWORD") {
		t.Fatalf("redis-exporter unit wrong:\n%s", unit)
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

func TestStackRendersAgent(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	ctx := baseContext()
	ctx.Host.Name = "core1"
	ctx.Host.PrivateIP = "10.20.0.21"
	ctx.Host.Stacks = []string{"agent"}
	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog["agent"], ctx)
	if err != nil {
		t.Fatalf("Stack(agent) error = %v", err)
	}
	if u := string(files["/etc/containers/systemd/mksrv-node-exporter.container"]); !strings.Contains(u, "PublishPort=10.20.0.21:9100:9100") {
		t.Fatalf("node-exporter unit missing private-IP publish:\n%s", u)
	}
	if u := string(files["/etc/containers/systemd/mksrv-cadvisor.container"]); !strings.Contains(u, "PublishPort=10.20.0.21:8080:8080") {
		t.Fatalf("cadvisor unit missing private-IP publish:\n%s", u)
	}
}

func TestStackRendersMonitorFleetWide(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	stacksRoot := filepath.Clean(filepath.Join("..", "..", "stacks"))

	// No postgres cluster in the fleet: the `patroni` job must not render.
	ctx := baseContext()
	ctx.Host.Name = "appd"
	ctx.Host.PrivateIP = "10.20.0.30"
	ctx.Host.Stacks = []string{"monitor"}
	ctx.Fleet = []Member{
		{Name: "appd", PrivateIP: "10.20.0.30", Role: "data"},
		{Name: "edge", PrivateIP: "10.20.0.10", Role: "edge"},
	}
	files, err := Stack(stacksRoot, catalog["monitor"], ctx)
	if err != nil {
		t.Fatalf("Stack(monitor) error = %v", err)
	}
	p := string(files["/var/lib/mksrv/stacks/monitor/prometheus.yml"])
	for _, want := range []string{
		`targets: ["10.20.0.30:9100"]`,
		`host: "appd", role: "data"`,
		`targets: ["10.20.0.10:9100"]`,
		`targets: ["10.20.0.30:8080"]`,
		`targets: ["10.20.0.10:8080"]`,
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prometheus.yml missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "job_name: patroni") {
		t.Fatalf("prometheus.yml should not have a patroni job without a postgres cluster:\n%s", p)
	}

	// A postgres cluster in the fleet adds the patroni + postgres-exporter jobs
	// over its members; an openbao cluster adds the openbao job; `cache` adds
	// the redis-exporter job (M23 phase 2).
	ctx.StackMembers = map[string][]Member{
		"postgres": {
			{Name: "core1", PrivateIP: "10.20.0.21"},
			{Name: "core2", PrivateIP: "10.20.0.22"},
			{Name: "core3", PrivateIP: "10.20.0.23"},
		},
		"openbao": {
			{Name: "core1", PrivateIP: "10.20.0.21"},
			{Name: "core2", PrivateIP: "10.20.0.22"},
			{Name: "core3", PrivateIP: "10.20.0.23"},
		},
	}
	ctx.StackHosts = map[string]string{"cache": "10.20.0.30"}
	files, err = Stack(stacksRoot, catalog["monitor"], ctx)
	if err != nil {
		t.Fatalf("Stack(monitor, cluster) error = %v", err)
	}
	p = string(files["/var/lib/mksrv/stacks/monitor/prometheus.yml"])
	for _, want := range []string{
		"job_name: patroni", `targets: ["10.20.0.21:8008"]`, `targets: ["10.20.0.23:8008"]`,
		"job_name: postgres-exporter", `targets: ["10.20.0.22:9187"]`,
		"job_name: openbao", "metrics_path: /v1/sys/metrics", `targets: ["10.20.0.21:8200"]`,
		"job_name: redis-exporter", `targets: ["10.20.0.30:9121"]`,
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prometheus.yml missing %q:\n%s", want, p)
		}
	}

	// Grafana gets the dashboards volume + provisioning file, datasource uids
	// are fixed so bundled dashboard JSON can reference them.
	grafana := string(files["/etc/containers/systemd/mksrv-grafana.container"])
	if !strings.Contains(grafana, "/var/lib/mksrv/stacks/monitor/dashboards:/var/lib/mksrv-dashboards:Z,ro") {
		t.Fatalf("grafana unit missing dashboards volume:\n%s", grafana)
	}
	if ds := string(files["/var/lib/mksrv/stacks/monitor/grafana-datasource.yml"]); !strings.Contains(ds, "uid: prometheus") {
		t.Fatalf("grafana datasource missing fixed uid:\n%s", ds)
	}
	dash := string(files["/var/lib/mksrv/stacks/monitor/dashboards/fleet-overview.json"])
	var decoded map[string]any
	if err := json.Unmarshal([]byte(dash), &decoded); err != nil {
		t.Fatalf("fleet-overview.json is not valid JSON: %v\n%s", err, dash)
	}
	if !strings.Contains(dash, `"legendFormat": "{{host}}"`) {
		t.Fatalf("fleet-overview.json should keep the literal Prometheus legend placeholder:\n%s", dash)
	}
}

func TestStackStorageAndRetention(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	stacksRoot := filepath.Clean(filepath.Join("..", "..", "stacks"))

	ctx := baseContext()
	ctx.Host.Name = "data"
	ctx.Host.PrivateIP = "10.20.0.168"
	ctx.Host.Stacks = []string{"monitor", "logs"}
	ctx.Retention = (&model.RetentionConfig{MetricsDays: 30, LogsDays: 21}).Resolved()

	mon, err := Stack(stacksRoot, catalog["monitor"], ctx)
	if err != nil {
		t.Fatalf("Stack(monitor) error = %v", err)
	}
	pu := string(mon["/etc/containers/systemd/mksrv-prometheus.container"])
	if !strings.Contains(pu, "Volume=/var/lib/mksrv/vol/tsdb:/prometheus:Z") {
		t.Fatalf("prometheus unit not on the dedicated volume:\n%s", pu)
	}
	if !strings.Contains(pu, "--storage.tsdb.retention.time=30d") {
		t.Fatalf("prometheus retention not templated:\n%s", pu)
	}

	lg, err := Stack(stacksRoot, catalog["logs"], ctx)
	if err != nil {
		t.Fatalf("Stack(logs) error = %v", err)
	}
	if lu := string(lg["/etc/containers/systemd/mksrv-loki.container"]); !strings.Contains(lu, "Volume=/var/lib/mksrv/vol/chunks:/loki:Z") {
		t.Fatalf("loki unit not on the dedicated volume:\n%s", lu)
	}
	if ly := string(lg["/var/lib/mksrv/stacks/logs/loki.yml"]); !strings.Contains(ly, "retention_period: 504h") { // 21 * 24
		t.Fatalf("loki retention not templated:\n%s", ly)
	}

	// postgres cluster
	if s := catalog["postgres"].Storage; len(s) != 2 || s[0].Name != "pgdata" || s[1].Name != "raft" {
		t.Fatalf("postgres storage block: %#v", s)
	}
	ctx.Host.Stacks = []string{"postgres"}
	ctx.StackMembers = map[string][]Member{"postgres": {{Name: "data", PrivateIP: "10.20.0.168"}}}
	pg, err := Stack(stacksRoot, catalog["postgres"], ctx)
	if err != nil {
		t.Fatalf("Stack(postgres) error = %v", err)
	}
	if u := string(pg["/etc/containers/systemd/mksrv-patroni.container"]); !strings.Contains(u, "Volume=/var/lib/mksrv/vol/pgdata:/var/lib/postgresql/data:Z") {
		t.Fatalf("patroni unit not on the dedicated volume:\n%s", u)
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

func TestStackPeersExcludesSelf(t *testing.T) {
	t.Parallel()
	ctx := baseContext()
	ctx.Host.Name = "pg2"
	ctx.StackMembers = map[string][]Member{"postgres": {
		{Name: "pg1", PrivateIP: "10.20.0.11"},
		{Name: "pg2", PrivateIP: "10.20.0.12"},
		{Name: "pg3", PrivateIP: "10.20.0.13"},
	}}
	if len(ctx.StackNodes("postgres")) != 3 {
		t.Fatalf("StackNodes = %d", len(ctx.StackNodes("postgres")))
	}
	peers := ctx.StackPeers("postgres")
	if len(peers) != 2 || peers[0].Name != "pg1" || peers[1].Name != "pg3" {
		t.Fatalf("StackPeers wrong: %+v", peers)
	}
}

func TestStackRendersPostgresCluster(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if catalog["postgres"].Kind != "cluster" {
		t.Fatalf("postgres stack kind = %q", catalog["postgres"].Kind)
	}
	members := []Member{
		{Name: "pg1", PrivateIP: "10.20.0.11"},
		{Name: "pg2", PrivateIP: "10.20.0.12"},
		{Name: "pg3", PrivateIP: "10.20.0.13"},
	}
	ctx := baseContext()
	ctx.Host.Name = "pg2"
	ctx.Host.PrivateIP = "10.20.0.12"
	ctx.Host.Stacks = []string{"postgres"}
	ctx.StackMembers = map[string][]Member{"postgres": members}

	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog["postgres"], ctx)
	if err != nil {
		t.Fatalf("Stack(postgres) error = %v", err)
	}
	y := string(files["/var/lib/mksrv/stacks/postgres/patroni.yml"])
	for _, want := range []string{
		"name: pg2",
		"self_addr: 10.20.0.12:2222",
		"- 10.20.0.11:2222",
		"- 10.20.0.13:2222",
	} {
		if !strings.Contains(y, want) {
			t.Fatalf("patroni.yml missing %q:\n%s", want, y)
		}
	}
	if strings.Contains(y, "- 10.20.0.12:2222") {
		t.Fatalf("patroni.yml lists self as a partner:\n%s", y)
	}
	if u := string(files["/etc/containers/systemd/mksrv-patroni.container"]); !strings.Contains(u, "PublishPort=10.20.0.12:8008:8008") {
		t.Fatalf("patroni unit wrong:\n%s", u)
	}
	if u := string(files["/etc/containers/systemd/mksrv-postgres-exporter.container"]); !strings.Contains(u, "Network=host") ||
		!strings.Contains(u, "DATA_SOURCE_URI=127.0.0.1:5432/postgres?sslmode=disable") ||
		!strings.Contains(u, "target=DATA_SOURCE_PASS") {
		t.Fatalf("postgres-exporter unit wrong:\n%s", u)
	}
}

func TestStackRendersBackup(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	ctx := baseContext()
	ctx.Host.Name = "appd"
	ctx.Host.Stacks = []string{"backup"}
	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog["backup"], ctx)
	if err != nil {
		t.Fatalf("Stack(backup) error = %v", err)
	}
	sh := string(files["/var/lib/mksrv/stacks/backup/backup.sh"])
	for _, want := range []string{
		"pg_dump -Fc",
		"bao operator raft snapshot save",
		"partial-export?exportClients=true",
		"restic forget --tag mksrv --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune",
		"--secret mksrv-backup-restic_password,type=env,target=RESTIC_PASSWORD",
	} {
		if !strings.Contains(sh, want) {
			t.Fatalf("backup.sh missing %q", want)
		}
	}
	if svc := string(files["/etc/systemd/system/mksrv-backup.service"]); !strings.Contains(svc, "Type=oneshot") {
		t.Fatalf("backup service wrong:\n%s", svc)
	}
	if tmr := string(files["/etc/systemd/system/mksrv-backup.timer"]); !strings.Contains(tmr, "OnCalendar=*-*-* 06:00:00") {
		t.Fatalf("backup timer wrong:\n%s", tmr)
	}
}

func TestStackRendersOpenBaoCluster(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if catalog["openbao"].Kind != "cluster" {
		t.Fatalf("openbao stack kind = %q", catalog["openbao"].Kind)
	}
	members := []Member{
		{Name: "bao1", PrivateIP: "10.20.0.21", TailnetIP: "100.64.0.21"},
		{Name: "bao2", PrivateIP: "10.20.0.22", TailnetIP: "100.64.0.22"},
		{Name: "bao3", PrivateIP: "10.20.0.23", TailnetIP: "100.64.0.23"},
	}
	ctx := baseContext()
	ctx.Region = "us-east-1"
	ctx.OpenBaoKMSKeyID = "arn:aws:kms:us-east-1:0:key/abc-123"
	ctx.Host.Name = "bao2"
	ctx.Host.PrivateIP = "10.20.0.22"
	ctx.Host.TailnetIP = "100.64.0.22"
	ctx.Host.Stacks = []string{"openbao"}
	ctx.StackMembers = map[string][]Member{"openbao": members}

	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog["openbao"], ctx)
	if err != nil {
		t.Fatalf("Stack(openbao) error = %v", err)
	}
	hcl := string(files["/var/lib/mksrv/stacks/openbao/openbao.hcl"])
	for _, want := range []string{
		`node_id = "bao2"`,
		`leader_api_addr = "http://10.20.0.21:8200"`,
		`leader_api_addr = "http://10.20.0.23:8200"`,
		`seal "awskms"`,
		`region     = "us-east-1"`,
		`kms_key_id = "arn:aws:kms:us-east-1:0:key/abc-123"`,
		`api_addr     = "http://10.20.0.22:8200"`,
		`unauthenticated_metrics_access = true`,
		`prometheus_retention_time = "24h"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Fatalf("openbao.hcl missing %q:\n%s", want, hcl)
		}
	}
	if strings.Count(hcl, "retry_join") != 3 {
		t.Fatalf("openbao.hcl want 3 retry_join, got %d:\n%s", strings.Count(hcl, "retry_join"), hcl)
	}
	unit := string(files["/etc/containers/systemd/mksrv-openbao.container"])
	for _, want := range []string{
		"AddCapability=IPC_LOCK",
		"Volume=/var/lib/mksrv/vol/baoraft:/openbao/data:Z",
		"PublishPort=10.20.0.22:8200:8200",
		"PublishPort=100.64.0.22:8200:8200",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("openbao unit missing %q:\n%s", want, unit)
		}
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
