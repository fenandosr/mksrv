// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fenandosr/mksrv/internal/render"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

// postgrestBasePort is the first host port assigned to a tenant PostgREST
// container. Tenants take consecutive ports in sorted-id order; the Headscale
// ACL opens 3010-3019, so this supports up to ten tenants.
const postgrestBasePort = 3010

// postgrestPort returns the host port for a tenant's PostgREST container, or 0
// when the tenant is not in the sorted list.
func postgrestPort(sortedIDs []string, id string) int {
	i := slices.Index(sortedIDs, id)
	if i < 0 {
		return 0
	}
	return postgrestBasePort + i
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
	for _, id := range tenants {
		if slices.Contains(f.data.Tenants[id].Stacks, "database") {
			consumers = append(consumers, id)
		}
	}
	if len(consumers) == 0 {
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

	dep := f.data.Deployment
	sortedIDs := sortedTenantIDs(f.data.Tenants)
	rctxBase := f.renderContext(*dataHost)
	dataPrivateIP := rctxBase.Host.PrivateIP

	for _, id := range consumers {
		tenant := f.data.Tenants[id]
		port := postgrestPort(sortedIDs, id)
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

	if _, err := client.Run(ctx, "sudo systemctl daemon-reload"); err != nil {
		return err
	}
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
		port := postgrestPort(sortedIDs, id)
		if _, err := client.Run(ctx, fmt.Sprintf(
			"curl -fsS -m 10 --retry 12 --retry-delay 3 --retry-all-errors http://127.0.0.1:%d/ -o /dev/null", port)); err != nil {
			return fmt.Errorf("postgrest %s did not become healthy: %w", id, err)
		}
		printer.Success("tenant %s: postgrest on :%d -> https://%s.rest.%s", id, port, id, dep.DNS.RootDomain)
	}
	return nil
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
