// SPDX-License-Identifier: Apache-2.0

package render

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/engine"
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
