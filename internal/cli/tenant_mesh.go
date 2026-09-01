// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/fenandosr/mksrv/internal/headscale"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

var meshNodeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// runTenantMesh mints a Headscale pre-auth key under the tenant's own Headscale
// user so a tenant-owned node (e.g. an HPC login node) can join the mesh. The
// operator runs the printed `tailscale up` command on that node; afterwards the
// node is reachable at <hostname>.<env>.mksrv and can be referenced from the
// tenant's forwards.
func (a *App) runTenantMesh(ctx context.Context, printer ui.Printer, globals *globalOptions, id, hostname string, reusable bool, ttl time.Duration) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	if _, ok := f.data.Tenants[id]; !ok {
		return &ExitError{Code: 2, Err: fmt.Errorf("unknown tenant %q", id)}
	}
	if hostname == "" {
		hostname = id + "-node"
	}
	if !meshNodeNameRe.MatchString(hostname) {
		return &ExitError{Code: 2, Err: fmt.Errorf("hostname %q must match %s", hostname, meshNodeNameRe.String())}
	}
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}

	edge := f.identityHost()
	if edge == nil {
		return &ExitError{Code: 2, Err: fmt.Errorf("no host carries identity")}
	}
	edgeClient, err := sshx.Dial(ctx, edge.Target, f.knownHosts)
	if err != nil {
		return dialError(edge.Name, err)
	}
	defer edgeClient.Close()

	hs := headscale.New(edgeClient)
	uid, err := hs.EnsureUser(ctx, id)
	if err != nil {
		return &ExitError{Code: 1, Err: fmt.Errorf("headscale user %s: %w", id, err)}
	}
	key, err := hs.PreAuthKey(ctx, uid, ttl, reusable)
	if err != nil {
		return &ExitError{Code: 1, Err: fmt.Errorf("mint pre-auth key: %w", err)}
	}

	dep := f.data.Deployment
	loginServer := "https://" + dep.Identity.HeadscaleDomain
	magicDNS := fmt.Sprintf("%s.%s.mksrv", hostname, dep.Env)
	upCmd := fmt.Sprintf("sudo tailscale up --login-server %s --authkey %s --hostname %s", loginServer, key, hostname)

	if printer.JSON {
		return printer.Encode(map[string]any{
			"tenant":       id,
			"hostname":     hostname,
			"magicdns":     magicDNS,
			"preauthKey":   key,
			"reusable":     reusable,
			"ttl":          ttl.String(),
			"tailscale_up": upCmd,
		})
	}

	printer.Info("run this on the %s node (install Tailscale first):", id)
	printer.Info("  %s", upCmd)
	printer.Info("")
	printer.Info("after it joins, the node is reachable at %s", magicDNS)
	printer.Info("add a forwards: entry targeting %s:<port> to tenants/%s.yaml and re-run `mksrv tenant apply`", magicDNS, id)
	if len(f.data.Tenants[id].MeshRoutes) > 0 {
		printer.Info("for the declared mesh_routes, also run on the edge: headscale nodes approve-routes --identifier <node-id> --routes %v", f.data.Tenants[id].MeshRoutes)
	}
	return nil
}
