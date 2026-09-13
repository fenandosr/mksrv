// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

// provisionRedis rewrites the shared Redis ACL file with one login user per
// tenant that consumes the `cache` stack and reloads it (ACL LOAD). Each tenant
// user is confined to the `<id>:*` key and channel namespace and cannot run
// dangerous or admin commands. It is idempotent.
func (f *fleet) provisionRedis(ctx context.Context, printer ui.Printer, tenants []string) error {
	var dataHost *hostTarget
	for i := range f.targets {
		if slices.Contains(f.targets[i].Host.Stacks, "cache") {
			dataHost = &f.targets[i]
			break
		}
	}
	if dataHost == nil {
		return nil
	}

	consumers := make([]string, 0, len(tenants))
	for _, id := range tenants {
		if slices.Contains(f.data.Tenants[id].Stacks, "cache") {
			consumers = append(consumers, id)
		}
	}
	if len(consumers) == 0 {
		return nil
	}

	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}
	admin, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/cache/redis_admin_pass", 32)
	if err != nil {
		return fmt.Errorf("redis admin password: %w", err)
	}

	// Every tenant that consumes cache gets a line, not just the selected
	// subset, so a partial `tenant apply` never drops another tenant's user.
	lines := []string{
		"user default off",
		fmt.Sprintf("user mksrv on >%s ~* &* +@all", admin),
	}
	for _, id := range sortedTenantIDs(f.data.Tenants) {
		if !slices.Contains(f.data.Tenants[id].Stacks, "cache") {
			continue
		}
		pw, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/cache/redis_"+id+"_password", 24)
		if err != nil {
			return fmt.Errorf("redis password for %s: %w", id, err)
		}
		lines = append(lines, redisACLLine(id, pw))
	}

	client, err := sshx.Dial(ctx, dataHost.Target, f.knownHosts)
	if err != nil {
		return dialError(dataHost.Name, err)
	}
	defer client.Close()

	content := strings.Join(lines, "\n") + "\n"
	if err := client.WriteFileSudo(ctx, "/var/lib/mksrv/stacks/cache/acl/users.acl", []byte(content), 0o644); err != nil {
		return fmt.Errorf("write redis acl: %w", err)
	}
	if _, err := client.Run(ctx, "sudo podman exec mksrv-redis redis-cli -a "+quoteArg(admin)+
		" --user mksrv --no-auth-warning ACL LOAD"); err != nil {
		return fmt.Errorf("redis ACL LOAD: %w", err)
	}

	for _, id := range consumers {
		printer.Success("tenant %s: redis ACL user %s (namespace %s:*)", id, id, id)
	}
	return nil
}

// celeryBookkeepingKeys are the fixed or per-process-dynamic, unprefixed
// KEY names Kombu's/Celery's Redis transport uses for its own broker
// bookkeeping, none of which respect
// `broker_transport_options={"global_keyprefix": "<id>:"}`:
//   - "unacked"/"unacked_index" track in-flight deliveries, "unacked_mutex"
//     guards restoring them on worker startup (Mutex() is handed the
//     underlying client directly, bypassing the prefixing wrapper);
//   - "_kombu.binding.*" holds queue/exchange bindings;
//   - "*.reply.celery.pidbox" is each worker's remote-control reply queue,
//     named with a per-process UUID Celery generates at startup.
//
// Confirmed live: a tenant's Celery worker crashed on every start with
// redis.exceptions.NoPermissionError until these key grants were in place.
// Granted to every tenant, not gated on actual Celery use — harmless if
// unused.
//
// Caveat: these are not tenant-namespaced (Kombu/Celery hardcode them), so
// two tenants both running Celery against this same shared Redis would
// collide on them. Only one tenant uses Celery today; if a second one
// starts, the real fix is a dedicated Redis DB per tenant (Redis databases
// are fully separate keyspaces, so unprefixed keys stop colliding) rather
// than growing this list further.
const celeryBookkeepingKeys = "~unacked ~unacked_index ~unacked_mutex ~_kombu.binding.* ~*.reply.celery.pidbox*"

// redisACLLine builds one aclfile `user` directive: enabled, password-only,
// scoped to the tenant's key namespace (plus the fixed Celery/Kombu
// bookkeeping keys above), every command except the dangerous and admin
// categories.
//
// Channels are UNSCOPED (`&*`, not `&<id>:*`): Celery/Kombu's pub/sub usage
// around the pidbox reply mailbox kept intermittently failing with "No
// permissions to access a channel" even after explicitly granting
// `&*.reply.celery.pidbox*` and confirming live, via a throwaway script
// using the exact same credential, that subscribing to that literal
// channel pattern worked — a worker recreated with that grant ran clean
// for several minutes, then crash-looped on the same error anyway, and no
// narrower pattern was found that didn't eventually recur. Pub/sub channels
// carry no persisted tenant data (fire-and-forget signaling, not the KV
// store itself), so the isolation cost of leaving them unscoped is low;
// keys (`~<id>:*`, where a tenant's actual cached/queued data lives) stay
// strictly scoped.
func redisACLLine(id, password string) string {
	return fmt.Sprintf("user %s on >%s resetchannels ~%s:* %s &* +@all -@dangerous -@admin",
		id, password, id, celeryBookkeepingKeys)
}
