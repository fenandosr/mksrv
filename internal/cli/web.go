// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/fenandosr/mksrv/internal/keycloak"
	"github.com/fenandosr/mksrv/internal/model"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

var webSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// oauth2ProxyImage gates SSO web endpoints (ADR 0030). Pinned; auth-only mode.
const oauth2ProxyImage = "quay.io/oauth2-proxy/oauth2-proxy:v7.7.1"

// webSSOBasePort is the first loopback port an edge oauth2-proxy binds. One per
// SSO tenant, in sorted-id order.
const webSSOBasePort = 4180

// webFragmentPath is the edge Caddy fragment for one tenant web endpoint. The
// 25- prefix orders it after identity (10-) and PostgREST (21-).
func webFragmentPath(id, hostname string) string {
	slug := strings.Trim(webSlugRe.ReplaceAllString(strings.ToLower(hostname), "-"), "-")
	return fmt.Sprintf("/var/lib/mksrv/caddy.d/25-web-%s-%s.caddy", id, slug)
}

func webSSOClientID(id string) string { return id + "-websso" }

// webSSOPort is the loopback port of tenant id's edge oauth2-proxy.
func webSSOPort(sortedIDs []string, id string) int {
	i := slices.Index(sortedIDs, id)
	if i < 0 {
		return webSSOBasePort
	}
	return webSSOBasePort + i
}

// webSSOHostnames returns the tenant's SSO web hostnames, sorted.
func webSSOHostnames(t model.Tenant) []string {
	var hs []string
	for _, w := range t.Web {
		if w.SSO && (w.Provider == "" || w.Provider == "edge") {
			hs = append(hs, w.Hostname)
		}
	}
	slices.Sort(hs)
	return hs
}

// webSSORedirectURIs is the Keycloak client's allowed callback list.
func webSSORedirectURIs(t model.Tenant) []string {
	var u []string
	for _, h := range webSSOHostnames(t) {
		u = append(u, "https://"+h+"/oauth2/callback")
	}
	return u
}

// webFragment renders the vhost. The edge terminates TLS (HTTP-01, its :80 is
// public) and reverse-proxies to the tenant's origin over the mesh (ADR 0028).
// The proxy block is tuned for interactive tenant apps:
//   - `header_up Host {host}` — a chained proxy at the origin routes by the real host;
//   - `header_up X-Forwarded-Proto {scheme}` — OIDC redirects, OnlyOffice;
//   - `flush_interval -1` — SSE / long-poll / chunked UIs stream through.
func webFragment(w model.TenantWebEndpoint, ssoPort int) string {
	proxy := fmt.Sprintf(`reverse_proxy %s {
		header_up Host {host}
		header_up X-Forwarded-Proto {scheme}
		flush_interval -1
	}`, w.Target)

	if !w.SSO {
		return fmt.Sprintf("%s {\n\t%s\n}\n", w.Hostname, proxy)
	}

	// Gate on a Keycloak session (ADR 0030): oauth2-proxy on the edge answers
	// /oauth2/*; an unauthenticated request is bounced to /oauth2/start.
	// `sso_groups` is enforced per hostname via oauth2-proxy's `allowed_groups`
	// query param — one proxy, different rules per vhost.
	authURI := "/oauth2/auth"
	if len(w.SSOGroups) > 0 {
		authURI += "?allowed_groups=" + strings.Join(w.SSOGroups, ",")
	}
	return fmt.Sprintf(`%s {
	handle /oauth2/* {
		reverse_proxy 127.0.0.1:%d
	}
	handle {
		forward_auth 127.0.0.1:%d {
			uri %s
			copy_headers X-Auth-Request-User X-Auth-Request-Email X-Auth-Request-Groups
			@err status 401 403
			handle_response @err {
				redir * /oauth2/start?rd={scheme}://{host}{uri}
			}
		}
		%s
	}
}
`, w.Hostname, ssoPort, ssoPort, authURI, proxy)
}

// webSSOContainer renders the per-tenant oauth2-proxy Quadlet for the edge.
func webSSOContainer(t model.Tenant, id, keycloakDomain, realm string, port int) string {
	redirect := ""
	if h := webSSOHostnames(t); len(h) > 0 {
		redirect = "https://" + h[0] + "/oauth2/callback"
	}

	return fmt.Sprintf(`[Unit]
Description=mksrv web SSO gate (%[1]s)
Requires=mksrv-keycloak.service
After=mksrv-keycloak.service
# After= only waits for Keycloak's unit to start, not for its HTTP endpoint
# to actually answer -- Keycloak (a JVM app) can take well past systemd's
# default 5-attempts-per-10s restart budget to finish booting, so
# oauth2-proxy's own OIDC discovery at startup (it does not retry
# internally) exhausts that budget and lands in "failed", needing a manual
# reset-failed + restart, purely because Keycloak was not ready yet.
# Confirmed live, twice. No rate limit here: Restart=always is meant to be
# self-healing once Keycloak is actually up, however long that takes;
# RestartSec spaces out attempts so it does not hammer Keycloak with a
# discovery request every default 100ms while it is still booting.
StartLimitIntervalSec=0

[Container]
ContainerName=mksrv-websso-%[1]s
Image=%[2]s
Network=host
Environment=OAUTH2_PROXY_HTTP_ADDRESS=127.0.0.1:%[3]d
Environment=OAUTH2_PROXY_PROVIDER=keycloak-oidc
Environment=OAUTH2_PROXY_OIDC_ISSUER_URL=https://%[4]s/realms/%[5]s
Environment=OAUTH2_PROXY_CLIENT_ID=%[1]s-websso
Environment=OAUTH2_PROXY_REDIRECT_URL=%[6]s
Environment=OAUTH2_PROXY_SCOPE=openid email profile
Environment=OAUTH2_PROXY_EMAIL_DOMAINS=*
Environment=OAUTH2_PROXY_COOKIE_DOMAINS=.%[7]s
Environment=OAUTH2_PROXY_WHITELIST_DOMAINS=.%[7]s
Environment=OAUTH2_PROXY_COOKIE_SECURE=true
Environment=OAUTH2_PROXY_REVERSE_PROXY=true
Environment=OAUTH2_PROXY_SET_XAUTHREQUEST=true
Environment=OAUTH2_PROXY_PASS_ACCESS_TOKEN=true
Environment=OAUTH2_PROXY_SKIP_PROVIDER_BUTTON=true
Environment=OAUTH2_PROXY_UPSTREAMS=static://202
Secret=mksrv-websso-%[1]s-oidc,type=env,target=OAUTH2_PROXY_CLIENT_SECRET
Secret=mksrv-websso-%[1]s-cookie,type=env,target=OAUTH2_PROXY_COOKIE_SECRET

[Service]
Restart=always
RestartSec=5s
TimeoutStartSec=60

[Install]
WantedBy=multi-user.target
`, id, oauth2ProxyImage, port, keycloakDomain, realm, redirect, t.BaseDomain)
}

// provisionTenantWeb reconciles every tenant's `web:` block on the edge (ADR
// 0028 + 0030): a Caddy vhost fragment per hostname, and — for tenants with any
// `sso: true` entry — an oauth2-proxy container gating those hostnames on a
// Keycloak session. Fragments and containers for dropped entries are removed.
// The matching `hostname -> edge EIP` DNS records come from Terraform's
// dns_tenant module (`mksrv apply --infra-only`).
func (f *fleet) provisionTenantWeb(ctx context.Context, printer ui.Printer, edgeClient *sshx.Client, kc *keycloak.Client, tenants []string) error {
	edge := f.identityHost()
	if edge == nil || len(tenants) == 0 {
		return nil
	}
	sortedIDs := sortedTenantIDs(f.data.Tenants)

	fragsChanged, unitsChanged := 0, 0
	var restartUnits []string

	for _, id := range tenants {
		t := f.data.Tenants[id]
		ssoPort := webSSOPort(sortedIDs, id)

		// The oauth2-proxy container, when the tenant gates any hostname.
		unitPath := "/etc/containers/systemd/mksrv-websso-" + id + ".container"
		if t.WebSSO() {
			if err := f.ensureSecrets(ctx); err != nil {
				return err
			}
			realm := tenantRealm(t)
			secret, err := kc.EnsureClient(ctx, realm, keycloak.ClientSpec{
				ClientID: webSSOClientID(id), Public: false,
				RedirectURIs: webSSORedirectURIs(t),
			})
			if err != nil {
				return fmt.Errorf("tenant %s: websso keycloak client: %w", id, err)
			}
			cookie, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/identity/websso_"+id+"_cookie", 32)
			if err != nil {
				return err
			}
			for name, val := range map[string]string{
				"mksrv-websso-" + id + "-oidc":   secret,
				"mksrv-websso-" + id + "-cookie": cookie,
			} {
				if _, err := edgeClient.RunInput(ctx,
					"sudo podman secret create --replace "+quoteArg(name)+" -", []byte(val)); err != nil {
					return fmt.Errorf("tenant %s: push %s: %w", id, name, err)
				}
			}
			unit := webSSOContainer(t, id, f.data.Deployment.Identity.KeycloakDomain, realm, ssoPort)
			old, _ := edgeClient.Run(ctx, "sudo cat "+unitPath+" 2>/dev/null || true")
			if old.Stdout != unit {
				if err := edgeClient.WriteFileSudo(ctx, unitPath, []byte(unit), 0o644); err != nil {
					return err
				}
				unitsChanged++
				restartUnits = append(restartUnits, "mksrv-websso-"+id+".service")
			}
		} else {
			// No SSO entry left — tear the gate down.
			res, _ := edgeClient.Run(ctx, "test -f "+unitPath+" && echo yes || true")
			if strings.TrimSpace(res.Stdout) == "yes" {
				_, _ = edgeClient.Run(ctx, "sudo systemctl disable --now mksrv-websso-"+id+".service 2>/dev/null || true")
				_, _ = edgeClient.Run(ctx, "sudo rm -f "+unitPath)
				_, _ = edgeClient.Run(ctx, "sudo podman secret rm mksrv-websso-"+id+"-oidc mksrv-websso-"+id+"-cookie 2>/dev/null || true")
				unitsChanged++
				printer.Success("tenant %s: web SSO gate torn down", id)
			}
		}

		// The Caddy fragments.
		want := map[string]string{}
		for _, w := range t.Web {
			if w.Provider != "" && w.Provider != "edge" {
				continue
			}
			want[webFragmentPath(id, w.Hostname)] = webFragment(w, ssoPort)
		}
		res, err := edgeClient.Run(ctx, fmt.Sprintf("ls /var/lib/mksrv/caddy.d/25-web-%s-*.caddy 2>/dev/null || true", quoteArg(id)))
		if err == nil {
			for _, line := range strings.Fields(res.Stdout) {
				if _, keep := want[line]; !keep {
					if _, err := edgeClient.Run(ctx, "sudo rm -f "+quoteArg(line)); err != nil {
						return fmt.Errorf("tenant %s: remove stale web vhost %s: %w", id, line, err)
					}
					fragsChanged++
				}
			}
		}
		for _, path := range sortedKeys(want) {
			if err := edgeClient.WriteFileSudo(ctx, path, []byte(want[path]), 0o644); err != nil {
				return fmt.Errorf("tenant %s: write web vhost %s: %w", id, path, err)
			}
			fragsChanged++
		}
		if len(want) > 0 {
			printer.Success("tenant %s: %d web vhost(s) on the edge%s", id, len(want), map[bool]string{true: " (SSO)", false: ""}[t.WebSSO()])
		}
	}

	if unitsChanged > 0 {
		if _, err := edgeClient.Run(ctx, "sudo systemctl daemon-reload"); err != nil {
			return err
		}
		for _, u := range restartUnits {
			if _, err := edgeClient.Run(ctx, "sudo systemctl restart "+u); err != nil {
				return fmt.Errorf("restart %s: %w", u, err)
			}
		}
	}
	if fragsChanged == 0 && unitsChanged == 0 {
		return nil
	}
	if _, err := edgeClient.Run(ctx,
		"sudo podman exec mksrv-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile || sudo systemctl restart mksrv-caddy.service"); err != nil {
		return fmt.Errorf("reload edge caddy for web vhosts: %w", err)
	}
	return nil
}
