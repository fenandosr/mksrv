// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/render"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

// postgrestBasePort is the first host port assigned to a tenant PostgREST
// container; postgrestPortCount ports are available (3010-3099, matching the
// Headscale ACL). Ports are persisted per tenant in mksrv.lock (ADR 0039 §6),
// so adding or removing a tenant never moves another tenant's live container.
const (
	postgrestBasePort  = 3010
	postgrestPortCount = 90
)

// postgrestConsumerIDs returns, sorted, every tenant in the whole workspace
// that actually gets a PostgREST container — `database` in `stacks:` and
// PostgRESTEnabled(). postgrestPort indexes into THIS list, not the raw
// tenant-id list and not the current run's `--tenant` selection: confirmed
// live (adding `gtex`, a `web:`-only tenant with no `database` stack) that
// indexing against every tenant shifts every actual PostgREST consumer's port
// whenever an unrelated tenant is inserted alphabetically before them — mcps
// kept its stale container on the old port (a partial `tenant apply` run
// stops at the first hard error, so a tenant later in sorted order than the
// one that fails never gets reconciled this run) while hg's freshly computed
// port collided with it, crash-looping hg's postgrest. Anchoring to the full
// set of consumers (never just `tenants`/the current selection) also keeps a
// partial `tenant apply <id>` from computing a different port than a full run
// would for that same tenant.
func postgrestConsumerIDs(tenants map[string]model.Tenant) []string {
	ids := make([]string, 0, len(tenants))
	for id, t := range tenants {
		if slices.Contains(t.Stacks, "database") && t.PostgRESTEnabled() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// assignStablePorts keeps every existing assignment for ids that are still
// consumers and gives each new one the lowest free port in [base, base+count),
// in sorted-id order. With an empty `existing` (a workspace that predates the
// lock entry) this reproduces the old alphabetical allocation, so live
// containers keep their ports. Assignments of ids that stopped being consumers
// are dropped, freeing their port.
func assignStablePorts(ids []string, base, count int, existing map[string]int, what string) (map[string]int, error) {
	out := make(map[string]int, len(ids))
	used := map[int]bool{}
	for _, id := range ids {
		p := existing[id]
		if p >= base && p < base+count && !used[p] {
			out[id] = p
			used[p] = true
		}
	}
	next := base
	for _, id := range ids {
		if _, ok := out[id]; ok {
			continue
		}
		for used[next] {
			next++
		}
		if next >= base+count {
			return nil, fmt.Errorf("no free %s port left (range %d-%d)", what, base, base+count-1)
		}
		out[id] = next
		used[next] = true
	}
	return out, nil
}

func assignPostgRESTPorts(tenants map[string]model.Tenant, existing map[string]int) (map[string]int, error) {
	return assignStablePorts(postgrestConsumerIDs(tenants), postgrestBasePort, postgrestPortCount, existing, "PostgREST")
}

// persistPorts writes ports into the workspace's mksrv.lock (through set) when
// they differ from existing, and keeps the in-memory lock in step.
func (f *fleet) persistPorts(ports, existing map[string]int, what string, set func(*model.Lock, map[string]int)) error {
	if maps.Equal(ports, existing) || (len(ports) == 0 && len(existing) == 0) {
		return nil
	}
	lock := model.Lock{Engine: strings.TrimSpace(f.data.Deployment.Engine)}
	if f.data.Lock != nil {
		lock = *f.data.Lock
	}
	if lock.Engine == "" {
		lock.Engine = "dev"
	}
	set(&lock, ports)
	if f.data.Root != "" {
		if err := writeLock(f.data.Root, lock); err != nil {
			return fmt.Errorf("persist %s ports to mksrv.lock: %w", what, err)
		}
	}
	f.data.Lock = &lock
	return nil
}

// postgrestPorts returns the stable per-tenant PostgREST ports, assigning and
// persisting any missing ones to mksrv.lock on first use.
func (f *fleet) postgrestPorts() (map[string]int, error) {
	var existing map[string]int
	if f.data.Lock != nil {
		existing = f.data.Lock.PostgRESTPorts
	}
	ports, err := assignPostgRESTPorts(f.data.Tenants, existing)
	if err != nil {
		return nil, err
	}
	if err := f.persistPorts(ports, existing, "postgrest", func(l *model.Lock, p map[string]int) { l.PostgRESTPorts = p }); err != nil {
		return nil, err
	}
	return ports, nil
}

// reconcilePostgREST deploys one PostgREST container per tenant that consumes
// the database stack. Each instance connects to db_<id> as the <id>_auth
// authenticator, verifies Keycloak tokens against the tenant realm's JWKS, and
// switches into the role named by the token's `role` claim. A per-tenant Caddy
// vhost (<id>.rest.<root_domain>) is written to the edge.
func (f *fleet) reconcilePostgREST(ctx context.Context, printer ui.Printer, edgeClient *sshx.Client, tenants []string) error {
	var dataHost *hostTarget
	for i := range f.targets {
		if slices.Contains(f.targets[i].Host.Stacks, "database") {
			dataHost = &f.targets[i]
			break
		}
	}
	if dataHost == nil {
		return nil
	}

	consumers := make([]string, 0, len(tenants))
	var disabled []string
	for _, id := range tenants {
		if !slices.Contains(f.data.Tenants[id].Stacks, "database") {
			continue
		}
		if f.data.Tenants[id].PostgRESTEnabled() {
			consumers = append(consumers, id)
		} else {
			disabled = append(disabled, id) // database: {postgrest: false} — ADR 0029
		}
	}
	if len(consumers) == 0 && len(disabled) == 0 {
		return nil
	}

	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}

	client, err := sshx.Dial(ctx, dataHost.Target, f.knownHosts)
	if err != nil {
		return dialError(dataHost.Name, err)
	}
	defer client.Close()

	// Tear down PostgREST for tenants that turned it off.
	for _, id := range disabled {
		_, _ = client.Run(ctx, "sudo systemctl disable --now mksrv-postgrest-"+id+".service 2>/dev/null || true")
		_, _ = client.Run(ctx, "sudo rm -f /etc/containers/systemd/mksrv-postgrest-"+id+".container")
		_, _ = client.Run(ctx, "sudo podman secret rm mksrv-database-postgrest-"+id+"-dburi mksrv-database-postgrest-"+id+"-jwt 2>/dev/null || true")
		_, _ = edgeClient.Run(ctx, "sudo rm -f /var/lib/mksrv/caddy.d/21-postgrest-"+id+".caddy")
		printer.Success("tenant %s: postgrest disabled (torn down)", id)
	}
	if len(consumers) == 0 {
		if len(postgrestConsumerIDs(f.data.Tenants)) == 0 {
			// Last PostgREST consumer gone: remove the JWKS refresh machinery too.
			_, _ = client.Run(ctx, "sudo systemctl disable --now mksrv-postgrest-jwks-refresh.timer 2>/dev/null || true")
			_, _ = client.Run(ctx, "sudo rm -f /etc/systemd/system/mksrv-postgrest-jwks-refresh.service /etc/systemd/system/mksrv-postgrest-jwks-refresh.timer "+jwksRefreshScriptPath+" "+jwksRefreshListPath)
		}
		_, _ = client.Run(ctx, "sudo systemctl daemon-reload")
		_, _ = edgeClient.Run(ctx, "sudo podman exec mksrv-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile || sudo systemctl restart mksrv-caddy.service")
		return nil
	}

	dep := f.data.Deployment
	ports, err := f.postgrestPorts()
	if err != nil {
		return err
	}
	rctxBase := f.renderContext(*dataHost)
	dataPrivateIP := rctxBase.Host.PrivateIP

	for _, id := range consumers {
		tenant := f.data.Tenants[id]
		port := ports[id]
		restFQDN := id + ".rest." + dep.DNS.RootDomain

		authPass, err := f.resolver.Get(ctx, "/mksrv/{env}/database/tenant_"+id+"_authpw")
		if err != nil {
			return fmt.Errorf("postgrest %s: authenticator password: %w (provision databases first)", id, err)
		}
		jwks, err := fetchRealmJWKS(ctx, dep.Identity.KeycloakDomain, tenantRealm(tenant))
		if err != nil {
			return fmt.Errorf("postgrest %s: %w", id, err)
		}
		dbURI := postgrestDSN(f.postgres, id, authPass)

		secrets := map[string]string{
			"mksrv-database-postgrest-" + id + "-dburi": dbURI,
			"mksrv-database-postgrest-" + id + "-jwt":   jwks,
		}
		for _, name := range sortedKeys(secrets) {
			if _, err := client.RunInput(ctx,
				"sudo podman secret create --replace "+quoteArg(name)+" -",
				[]byte(secrets[name]),
			); err != nil {
				return fmt.Errorf("postgrest %s: push %s: %w", id, name, err)
			}
		}

		rctx := rctxBase
		t := tenant
		rctx.Tenant = &t
		rctx.Secrets = map[string]string{
			"listen_port": strconv.Itoa(port),
			"rest_fqdn":   restFQDN,
			// ADR 0039: issuer for hooks that validate `iss`; only rendered
			// for tenants that use the extended PostgREST/RBAC config.
			"oidc_issuer": "https://" + dep.Identity.KeycloakDomain + "/realms/" + tenantRealm(tenant),
		}
		unit, err := render.One(
			filepath.Join(f.stacksRoot, "database", "templates", "postgrest.container.tmpl"),
			rctx,
		)
		if err != nil {
			return err
		}
		if err := client.WriteFileSudo(ctx,
			"/etc/containers/systemd/mksrv-postgrest-"+id+".container", unit, 0o644); err != nil {
			return err
		}

		frag := fmt.Sprintf("%s {\n\treverse_proxy %s:%d\n}\n", restFQDN, dataPrivateIP, port)
		if err := edgeClient.WriteFileSudo(ctx,
			"/var/lib/mksrv/caddy.d/21-postgrest-"+id+".caddy", []byte(frag), 0o644); err != nil {
			return err
		}
	}

	if err := installJWKSRefresh(ctx, client, jwksRefreshList(postgrestConsumerIDs(f.data.Tenants), f.data.Tenants, dep.Identity.KeycloakDomain)); err != nil {
		return fmt.Errorf("install postgrest JWKS refresh: %w", err)
	}
	if _, err := client.Run(ctx, "sudo systemctl daemon-reload"); err != nil {
		return err
	}
	_, _ = client.Run(ctx, "sudo systemctl enable --now mksrv-postgrest-jwks-refresh.timer")
	for _, id := range consumers {
		if _, err := client.Run(ctx, "sudo systemctl restart mksrv-postgrest-"+id+".service"); err != nil {
			return fmt.Errorf("restart postgrest %s: %w", id, err)
		}
	}
	_, _ = client.Run(ctx, "sudo podman network reload --all")
	if _, err := edgeClient.Run(ctx,
		"sudo podman exec mksrv-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile || sudo systemctl restart mksrv-caddy.service"); err != nil {
		return fmt.Errorf("reload edge caddy: %w", err)
	}

	for _, id := range consumers {
		port := ports[id]
		if _, err := client.Run(ctx, postgrestHealthCmd(port)); err != nil {
			return fmt.Errorf("postgrest %s did not become healthy: %w", id, err)
		}
		printer.Success("tenant %s: postgrest on :%d -> https://%s.rest.%s", id, port, id, dep.DNS.RootDomain)
	}
	return nil
}

// postgrestHealthCmd polls an anonymous GET / until PostgREST answers 200 (the
// OpenAPI document) or 404 (database.postgrest.openapi_mode: disabled). Anything
// else — 401/403 from a broken pre-request, 5xx from an unreachable database —
// keeps retrying and finally fails.
func postgrestHealthCmd(port int) string {
	return fmt.Sprintf(`for i in $(seq 1 13); do c=$(curl -sS -m 10 -o /dev/null -w '%%{http_code}' http://127.0.0.1:%d/ 2>/dev/null); case "$c" in 200|404) exit 0;; esac; [ "$i" -lt 13 ] && sleep 3; done; echo "last status: $c" >&2; exit 1`, port)
}

// fetchRealmJWKS returns the tenant realm's JWKS document, used verbatim as
// postgrestDSN is the libpq URI PostgREST connects with. With a Patroni cluster
// it lists every node and asks for the read-write one (the primary); otherwise
// it targets the `database` stack's standalone Postgres.
func postgrestDSN(c postgresCluster, id, authPass string) string {
	if len(c.Nodes) == 0 {
		return fmt.Sprintf("postgres://%s_auth:%s@mksrv-postgres:5432/db_%s", id, authPass, id)
	}
	hosts := make([]string, len(c.Nodes))
	for i, n := range c.Nodes {
		hosts[i] = n.IP + ":5432"
	}
	return fmt.Sprintf("postgres://%s_auth:%s@%s/db_%s?target_session_attrs=read-write",
		id, authPass, strings.Join(hosts, ","), id)
}

// PostgREST's PGRST_JWT_SECRET.
func fetchRealmJWKS(ctx context.Context, keycloakDomain, realm string) (string, error) {
	url := fmt.Sprintf("https://%s/realms/%s/protocol/openid-connect/certs", keycloakDomain, realm)
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch realm jwks: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch realm jwks %s: status %d", url, res.StatusCode)
	}
	return string(body), nil
}

// PostgREST validates tokens against a JWKS that mksrv bakes into a podman
// secret at `tenant apply` time. A Keycloak key rotation would otherwise break
// validation until the next apply (ADR 0039 §4). A systemd timer on the data
// host re-fetches each realm's JWKS and, only when it changed, replaces the
// secret and restarts that tenant's PostgREST. (A SIGUSR2 config reload would
// avoid the restart, but is unverified against the pinned image; this is the
// ADR's documented fallback and works on every version.)
const (
	jwksRefreshScriptPath = "/usr/local/bin/mksrv-postgrest-jwks-refresh"
	jwksRefreshListPath   = "/etc/mksrv/postgrest-jwks.list"
	jwksRefreshScript     = `#!/usr/bin/env bash
# Managed by mksrv (ADR 0039 §4). Re-fetch each tenant realm's JWKS; replace the
# podman secret and restart PostgREST only when it changed.
set -uo pipefail
rc=0
while read -r id url; do
  [ -n "${id:-}" ] || continue
  new=$(curl -fsS -m 20 "$url") || { echo "jwks fetch failed for $id" >&2; rc=1; continue; }
  case "$new" in *'"keys"'*) ;; *) echo "unexpected jwks body for $id" >&2; rc=1; continue ;; esac
  secret="mksrv-database-postgrest-$id-jwt"
  cur=$(podman secret inspect --showsecret --format '{{.SecretData}}' "$secret" 2>/dev/null || true)
  [ "$new" = "$cur" ] && continue
  printf '%s' "$new" | podman secret create --replace "$secret" - >/dev/null || { rc=1; continue; }
  systemctl restart "mksrv-postgrest-$id.service" || rc=1
  echo "rotated JWKS for $id"
done < ` + jwksRefreshListPath + `
exit $rc
`
	jwksRefreshService = `[Unit]
Description=mksrv PostgREST JWKS refresh
After=network-online.target

[Service]
Type=oneshot
ExecStart=` + jwksRefreshScriptPath + `
`
	jwksRefreshTimer = `[Unit]
Description=mksrv PostgREST JWKS refresh (hourly)

[Timer]
OnBootSec=5min
OnUnitActiveSec=1h
RandomizedDelaySec=120

[Install]
WantedBy=timers.target
`
)

// jwksRefreshList is the "<tenant> <jwks-url>" file the refresh script walks,
// one line per PostgREST consumer, sorted.
func jwksRefreshList(consumers []string, tenants map[string]model.Tenant, keycloakDomain string) string {
	ids := append([]string(nil), consumers...)
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "%s https://%s/realms/%s/protocol/openid-connect/certs\n", id, keycloakDomain, tenantRealm(tenants[id]))
	}
	return b.String()
}

func installJWKSRefresh(ctx context.Context, client *sshx.Client, list string) error {
	if _, err := client.Run(ctx, "sudo mkdir -p /etc/mksrv"); err != nil {
		return err
	}
	for path, f := range map[string]struct {
		body string
		mode os.FileMode
	}{
		jwksRefreshListPath:   {list, 0o644},
		jwksRefreshScriptPath: {jwksRefreshScript, 0o755},
		"/etc/systemd/system/mksrv-postgrest-jwks-refresh.service": {jwksRefreshService, 0o644},
		"/etc/systemd/system/mksrv-postgrest-jwks-refresh.timer":   {jwksRefreshTimer, 0o644},
	} {
		if err := client.WriteFileSudo(ctx, path, []byte(f.body), f.mode); err != nil {
			return err
		}
	}
	return nil
}
