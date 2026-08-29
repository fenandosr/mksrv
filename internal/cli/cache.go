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
	if err := client.WriteFileSudo(ctx, "/var/lib/mksrv/stacks/cache/users.acl", []byte(content), 0o644); err != nil {
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

// redisACLLine builds one aclfile `user` directive: enabled, password-only,
// scoped to the tenant's key and channel namespace, every command except the
// dangerous and admin categories.
func redisACLLine(id, password string) string {
	return fmt.Sprintf("user %s on >%s resetchannels ~%s:* &%s:* +@all -@dangerous -@admin",
		id, password, id, id)
}
