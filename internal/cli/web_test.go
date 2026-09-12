// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/model"
)

func TestWebFragment(t *testing.T) {
	t.Parallel()
	w := model.TenantWebEndpoint{Hostname: "files.mcps-epcm.org", Target: "mcps-nextcloud.prod.mksrv:80"}

	if got := webFragmentPath("mcps", w.Hostname); got != "/var/lib/mksrv/caddy.d/25-web-mcps-files-mcps-epcm-org.caddy" {
		t.Fatalf("fragment path = %q", got)
	}
	frag := webFragment(w, 4180)
	for _, want := range []string{
		"files.mcps-epcm.org {",
		"reverse_proxy mcps-nextcloud.prod.mksrv:80 {",
		"header_up Host {host}",
		"header_up X-Forwarded-Proto {scheme}",
		"flush_interval -1",
	} {
		if !strings.Contains(frag, want) {
			t.Fatalf("fragment missing %q:\n%s", want, frag)
		}
	}
	if strings.Contains(frag, "forward_auth") || strings.Contains(frag, "oauth2") {
		t.Fatalf("non-SSO fragment should not gate:\n%s", frag)
	}
}

func TestWebFragmentSSO(t *testing.T) {
	t.Parallel()
	w := model.TenantWebEndpoint{Hostname: "jupyter.mcps-epcm.org", Target: "mcps-hpc-01.prod.mksrv:8000", SSO: true}
	frag := webFragment(w, 4182)
	for _, want := range []string{
		"jupyter.mcps-epcm.org {",
		"handle /oauth2/* {",
		"reverse_proxy 127.0.0.1:4182",
		"forward_auth 127.0.0.1:4182 {",
		"uri /oauth2/auth\n",
		"copy_headers X-Auth-Request-User X-Auth-Request-Email X-Auth-Request-Groups",
		"@err status 401 403",
		"redir * /oauth2/start?rd={scheme}://{host}{uri}",
		"reverse_proxy mcps-hpc-01.prod.mksrv:8000 {",
		"flush_interval -1",
	} {
		if !strings.Contains(frag, want) {
			t.Fatalf("SSO fragment missing %q:\n%s", want, frag)
		}
	}

	// sso_groups -> per-hostname allowed_groups query param.
	gated := webFragment(model.TenantWebEndpoint{Hostname: "git.mcps-epcm.org", Target: "n:3000", SSO: true, SSOGroups: []string{"dev", "admin"}}, 4182)
	if !strings.Contains(gated, "uri /oauth2/auth?allowed_groups=dev,admin") {
		t.Fatalf("gated fragment missing allowed_groups:\n%s", gated)
	}
}

func TestWebSSOPort(t *testing.T) {
	t.Parallel()
	ids := []string{"acme", "bitabit", "mcps"}
	if got := webSSOPort(ids, "acme"); got != 4180 {
		t.Fatalf("acme port = %d", got)
	}
	if got := webSSOPort(ids, "mcps"); got != 4182 {
		t.Fatalf("mcps port = %d", got)
	}
	if got := webSSOPort(ids, "unknown"); got != 4180 {
		t.Fatalf("unknown port = %d, want base", got)
	}
}

func TestWebSSOContainer(t *testing.T) {
	t.Parallel()
	tn := model.Tenant{
		ID:         "mcps",
		BaseDomain: "mcps-epcm.org",
		Web: []model.TenantWebEndpoint{
			{Hostname: "git.mcps-epcm.org", Target: "n:3000", SSO: true, SSOGroups: []string{"dev", "admin"}},
			{Hostname: "jupyter.mcps-epcm.org", Target: "n:8000", SSO: true},
		},
	}
	unit := webSSOContainer(tn, "mcps", "auth.cloud-it.click", "mcps", 4182)
	for _, want := range []string{
		"ContainerName=mksrv-websso-mcps",
		"Image=quay.io/oauth2-proxy/oauth2-proxy:",
		"OAUTH2_PROXY_HTTP_ADDRESS=127.0.0.1:4182",
		"OAUTH2_PROXY_OIDC_ISSUER_URL=https://auth.cloud-it.click/realms/mcps",
		"OAUTH2_PROXY_CLIENT_ID=mcps-websso",
		"OAUTH2_PROXY_REDIRECT_URL=https://git.mcps-epcm.org/oauth2/callback", // sorted-first hostname
		"OAUTH2_PROXY_COOKIE_DOMAINS=.mcps-epcm.org",
		"Secret=mksrv-websso-mcps-oidc,type=env,target=OAUTH2_PROXY_CLIENT_SECRET",
		"Secret=mksrv-websso-mcps-cookie,type=env,target=OAUTH2_PROXY_COOKIE_SECRET",
		// After=mksrv-keycloak.service only waits for the unit to start, not
		// for Keycloak's HTTP endpoint to actually answer -- oauth2-proxy's
		// OIDC discovery at startup doesn't retry internally, so without an
		// unlimited restart budget it can exhaust systemd's default 5-in-10s
		// limit and land in 'failed' before a still-booting Keycloak is
		// ready (confirmed live, twice).
		"StartLimitIntervalSec=0",
		"RestartSec=5s",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("websso unit missing %q:\n%s", want, unit)
		}
	}
	// Groups are enforced per hostname in the fragment, not tenant-wide here.
	if strings.Contains(unit, "ALLOWED_GROUPS") {
		t.Fatalf("group enforcement belongs in the fragment, not the container:\n%s", unit)
	}
}

func TestWebOriginPorts(t *testing.T) {
	t.Parallel()
	got := webOriginPorts([]model.TenantWebEndpoint{
		{Target: "a.prod.mksrv:8000"},
		{Target: "b.prod.mksrv:80"},
		{Target: "c.prod.mksrv:80"}, // dup port
		{Target: "bad"},             // no port — skipped
	})
	if strings.Join(got, ",") != "80,8000" {
		t.Fatalf("ports = %v, want [80 8000]", got)
	}
}
