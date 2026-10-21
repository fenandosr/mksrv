// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"

	"github.com/fenandosr/mksrv/internal/render"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

// filesNextcloudBasePort and filesSambaBasePort are the first host ports
// assigned to a tenant's Nextcloud/Samba containers, same sorted-id-index
// scheme as postgrestBasePort — up to ten tenants each, the Headscale ACL
// opens the matching ranges (internal/headscale/headscale.go).
const (
	filesNextcloudBasePort = 8030
	filesSambaBasePort     = 4450
)

// filesConsumerIDs returns, sorted, every tenant in the whole workspace that
// carries the `files` stack — anchored to the full set for the same reason
// postgrestConsumerIDs is (internal/cli/postgrest.go): a partial `tenant apply
// <id>` must never shift another tenant's already-assigned port.
func (f *fleet) filesConsumerIDs() []string {
	ids := make([]string, 0, len(f.data.Tenants))
	for id, t := range f.data.Tenants {
		if slices.Contains(t.Stacks, "files") {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func filesPort(base int, sortedIDs []string, id string) int {
	i := slices.Index(sortedIDs, id)
	if i < 0 {
		return 0
	}
	return base + i
}

// provisionFilesTenants reconciles one Nextcloud + one Samba instance per
// tenant that carries the `files` stack (ADR 0040): a dedicated Postgres
// database for Nextcloud (dedicatedAppDatabaseSQL — this app owns its schema
// outright, no RBAC buckets), generated credentials in SSM, the two per-tenant
// Quadlet units, and an edge Caddy vhost for Nextcloud's web UI
// (<id>.files.<root_domain> — Samba has no HTTP story, tailnet/loopback only).
// Tenants in `tenants` that no longer carry `files` are torn down
// (best-effort, idempotent — a tenant that never had it is a silent no-op),
// mirroring reconcilePostgREST's `disabled` loop.
func (f *fleet) provisionFilesTenants(ctx context.Context, printer ui.Printer, edgeClient *sshx.Client, tenants []string) error {
	var dataHost *hostTarget
	for i := range f.targets {
		if slices.Contains(f.targets[i].Host.Stacks, "files") {
			dataHost = &f.targets[i]
			break
		}
	}
	if dataHost == nil {
		return nil
	}

	var consumers, disabled []string
	for _, id := range tenants {
		if slices.Contains(f.data.Tenants[id].Stacks, "files") {
			consumers = append(consumers, id)
		} else {
			disabled = append(disabled, id)
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

	for _, id := range disabled {
		_, _ = client.Run(ctx, "sudo systemctl disable --now mksrv-nextcloud-"+id+".service mksrv-samba-"+id+".service 2>/dev/null || true")
		_, _ = client.Run(ctx, "sudo rm -f /etc/containers/systemd/mksrv-nextcloud-"+id+".container /etc/containers/systemd/mksrv-samba-"+id+".container")
		_, _ = client.Run(ctx, "sudo podman secret rm mksrv-files-"+id+"-dbpass mksrv-files-"+id+"-adminpass mksrv-files-"+id+"-sambapass 2>/dev/null || true")
		if edgeClient != nil {
			_, _ = edgeClient.Run(ctx, "sudo rm -f /var/lib/mksrv/caddy.d/22-files-"+id+".caddy")
		}
		printer.Success("tenant %s: files disabled (torn down)", id)
	}
	if len(consumers) == 0 {
		_, _ = client.Run(ctx, "sudo systemctl daemon-reload")
		if edgeClient != nil {
			_, _ = edgeClient.Run(ctx, "sudo podman exec mksrv-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile || sudo systemctl restart mksrv-caddy.service")
		}
		return nil
	}

	pg, ok, err := f.pgConn()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("files: no postgres available (deploy database first)")
	}
	superPass, err := f.resolver.Get(ctx, pg.superRef)
	if err != nil {
		return fmt.Errorf("files: postgres superuser password: %w (deploy postgres first)", err)
	}
	pgClient, err := sshx.Dial(ctx, pg.target, f.knownHosts)
	if err != nil {
		return dialError(pg.name, err)
	}
	pg, pgClient = f.ensurePrimary(ctx, pgClient, pg)
	defer pgClient.Close()

	dep := f.data.Deployment
	sortedIDs := f.filesConsumerIDs()
	rctxBase := f.renderContext(*dataHost)

	for _, id := range consumers {
		tenant := f.data.Tenants[id]
		db := "nextcloud_" + id
		role := "nextcloud_" + id
		dbPass, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/files/tenant_"+id+"_db_password", 24)
		if err != nil {
			return err
		}
		adminPass, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/files/tenant_"+id+"_admin_password", 24)
		if err != nil {
			return err
		}
		sambaPass, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/files/tenant_"+id+"_samba_password", 24)
		if err != nil {
			return err
		}

		cmd := fmt.Sprintf(
			"sudo podman exec -e PGPASSWORD=%s -i %s psql -v ON_ERROR_STOP=1 -U %s -d postgres",
			quoteArg(superPass), pg.container, pg.psqlUser,
		)
		if _, err := pgClient.RunInput(ctx, cmd, []byte(dedicatedAppDatabaseSQL(role, db, dbPass))); err != nil {
			return fmt.Errorf("files %s: provision database: %w", id, err)
		}

		secrets := map[string]string{
			"mksrv-files-" + id + "-dbpass":    dbPass,
			"mksrv-files-" + id + "-adminpass": adminPass,
			"mksrv-files-" + id + "-sambapass": sambaPass,
		}
		for _, name := range sortedKeys(secrets) {
			if _, err := client.RunInput(ctx,
				"sudo podman secret create --replace "+quoteArg(name)+" -", []byte(secrets[name])); err != nil {
				return fmt.Errorf("files %s: push %s: %w", id, name, err)
			}
		}

		filesFQDN := id + ".files." + dep.DNS.RootDomain
		ncPort := filesPort(filesNextcloudBasePort, sortedIDs, id)
		smbPort := filesPort(filesSambaBasePort, sortedIDs, id)

		rctx := rctxBase
		t := tenant
		rctx.Tenant = &t
		rctx.Secrets = map[string]string{
			"nextcloud_port": strconv.Itoa(ncPort),
			"samba_port":     strconv.Itoa(smbPort),
			"pg_host":        pg.pgAdmin,
			"db_name":        db,
			"db_role":        role,
			"files_fqdn":     filesFQDN,
		}
		for app, tmplFile := range map[string]string{
			"nextcloud": "nextcloud.container.tmpl",
			"samba":     "samba.container.tmpl",
		} {
			unit, err := render.One(filepath.Join(f.stacksRoot, "files", "templates", tmplFile), rctx)
			if err != nil {
				return err
			}
			if err := client.WriteFileSudo(ctx,
				"/etc/containers/systemd/mksrv-"+app+"-"+id+".container", unit, 0o644); err != nil {
				return err
			}
		}

		if edgeClient != nil {
			dataPrivateIP := rctxBase.Host.PrivateIP
			frag := fmt.Sprintf("%s {\n\treverse_proxy %s:%d\n}\n", filesFQDN, dataPrivateIP, ncPort)
			if err := edgeClient.WriteFileSudo(ctx,
				"/var/lib/mksrv/caddy.d/22-files-"+id+".caddy", []byte(frag), 0o644); err != nil {
				return err
			}
		}
	}

	if _, err := client.Run(ctx, "sudo systemctl daemon-reload"); err != nil {
		return err
	}
	for _, id := range consumers {
		if _, err := client.Run(ctx, "sudo systemctl restart mksrv-nextcloud-"+id+".service mksrv-samba-"+id+".service"); err != nil {
			return fmt.Errorf("files %s: restart: %w", id, err)
		}
	}
	_, _ = client.Run(ctx, "sudo podman network reload --all")
	if edgeClient != nil {
		if _, err := edgeClient.Run(ctx,
			"sudo podman exec mksrv-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile || sudo systemctl restart mksrv-caddy.service"); err != nil {
			return fmt.Errorf("reload edge caddy: %w", err)
		}
	}

	for _, id := range consumers {
		ncPort := filesPort(filesNextcloudBasePort, sortedIDs, id)
		if _, err := client.Run(ctx, fmt.Sprintf(
			"curl -fsS -m 10 --retry 12 --retry-delay 3 --retry-all-errors http://127.0.0.1:%d/status.php -o /dev/null", ncPort)); err != nil {
			return fmt.Errorf("files %s: nextcloud did not become healthy: %w", id, err)
		}
		smbPort := filesPort(filesSambaBasePort, sortedIDs, id)
		if _, err := client.Run(ctx, fmt.Sprintf(
			"timeout 10 bash -c 'until cat < /dev/null > /dev/tcp/127.0.0.1/%d; do sleep 1; done'", smbPort)); err != nil {
			return fmt.Errorf("files %s: samba did not become healthy: %w", id, err)
		}
		printer.Success("tenant %s: files ready (nextcloud :%d -> https://%s.files.%s, samba :%d)",
			id, ncPort, id, f.data.Deployment.DNS.RootDomain, smbPort)
	}
	return nil
}
