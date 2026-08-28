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
