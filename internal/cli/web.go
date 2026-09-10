// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/fenandosr/mksrv/internal/model"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

var webSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// webFragmentPath is the edge Caddy fragment for one tenant web endpoint. The
// 25- prefix orders it after identity (10-) and PostgREST (21-).
func webFragmentPath(id, hostname string) string {
	slug := strings.Trim(webSlugRe.ReplaceAllString(strings.ToLower(hostname), "-"), "-")
	return fmt.Sprintf("/var/lib/mksrv/caddy.d/25-web-%s-%s.caddy", id, slug)
}

// webFragment renders the vhost: the edge terminates TLS (HTTP-01, its :80 is
// public) and reverse-proxies to the tenant's origin over the mesh (ADR 0028).
//
// The proxy block is tuned for interactive tenant apps (Jupyter, RStudio, Gitea,
// dashboards), which is what `web:` is for:
//   - `header_up Host {host}` — keep the original host so a chained proxy at the
//     origin (a tenant's own Caddy/nginx routing several vhosts) sees the real name;
//   - `header_up X-Forwarded-Proto {scheme}` — the client link is HTTPS at the
//     edge; apps that build absolute URLs (OIDC redirects, OnlyOffice) need this;
//   - `flush_interval -1` — stream responses through instead of buffering, so
//     SSE / long-poll / chunked UIs stay responsive. WebSockets already stream.
func webFragment(w model.TenantWebEndpoint) string {
	return fmt.Sprintf(`%s {
	reverse_proxy %s {
		header_up Host {host}
		header_up X-Forwarded-Proto {scheme}
		flush_interval -1
	}
}
`, w.Hostname, w.Target)
}

// provisionTenantWeb writes an edge Caddy vhost fragment for every `web:` entry
// a tenant declares (provider "edge"), removes fragments for entries that were
// deleted, and reloads Caddy once if anything changed (ADR 0028). The matching
// `hostname -> edge EIP` DNS records are written by Terraform's dns_tenant
// module; run `mksrv apply --infra-only` for those.
func (f *fleet) provisionTenantWeb(ctx context.Context, printer ui.Printer, edgeClient *sshx.Client, tenants []string) error {
	edge := f.identityHost()
	if edge == nil || len(tenants) == 0 {
		return nil
	}

	changed := 0
	for _, id := range tenants {
		t := f.data.Tenants[id]

		want := map[string]string{}
		for _, w := range t.Web {
			if w.Provider != "" && w.Provider != "edge" {
				continue
			}
			want[webFragmentPath(id, w.Hostname)] = webFragment(w)
		}

		// Drop fragments for hostnames this tenant no longer declares.
		res, err := edgeClient.Run(ctx, fmt.Sprintf("ls /var/lib/mksrv/caddy.d/25-web-%s-*.caddy 2>/dev/null || true", quoteArg(id)))
		if err == nil {
			for _, line := range strings.Fields(res.Stdout) {
				if _, keep := want[line]; !keep {
					if _, err := edgeClient.Run(ctx, "sudo rm -f "+quoteArg(line)); err != nil {
						return fmt.Errorf("tenant %s: remove stale web vhost %s: %w", id, line, err)
					}
					changed++
				}
			}
		}

		for _, path := range sortedKeys(want) {
			if err := edgeClient.WriteFileSudo(ctx, path, []byte(want[path]), 0o644); err != nil {
				return fmt.Errorf("tenant %s: write web vhost %s: %w", id, path, err)
			}
			changed++
		}
		if len(want) > 0 {
			printer.Success("tenant %s: %d web vhost(s) on the edge", id, len(want))
		}
	}

	if changed == 0 {
		return nil
	}
	if _, err := edgeClient.Run(ctx,
		"sudo podman exec mksrv-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile || sudo systemctl restart mksrv-caddy.service"); err != nil {
		return fmt.Errorf("reload edge caddy for web vhosts: %w", err)
	}
	return nil
}
