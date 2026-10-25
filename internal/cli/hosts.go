// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	awsclient "github.com/fenandosr/mksrv/internal/aws"
	"github.com/fenandosr/mksrv/internal/deploy"
	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/headscale"
	"github.com/fenandosr/mksrv/internal/infra"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/render"
	"github.com/fenandosr/mksrv/internal/schema"
	secretsx "github.com/fenandosr/mksrv/internal/secrets"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
	"github.com/fenandosr/mksrv/internal/workspace"
)

type hostTarget struct {
	Name   string
	Target sshx.Target
	Host   model.Host
	// Local marks a tenant's local_hosts entry (ADR 0034): no SSH, no
	// bootstrap, deployed in-process on whatever machine the CLI runs on.
	// Target is unused for these.
	Local bool
	// LocalHost and TenantID are set only when Local is true.
	LocalHost *model.TenantLocalHost
	TenantID  string
}

type fleet struct {
	data       workspace.Data
	catalog    map[string]model.Stack
	knownHosts string
	stacksRoot string
	targets    []hostTarget
	byName     map[string]hostTarget
	outputs    infra.Outputs
	resolver   *secretsx.Resolver
	awsClients *awsclient.Clients
	meshIPs    map[string]string
	postgres   postgresCluster
	openbao    openbaoCluster
}

// ensureSecrets loads AWS config and builds the SSM-backed secret resolver.
// It is called only by paths that deploy stacks.
func (f *fleet) ensureSecrets(ctx context.Context) error {
	if f.resolver != nil {
		return nil
	}
	clients, err := awsclient.Load(ctx, awsclient.Options{
		Region:  f.data.Deployment.AWS.Region,
		Profile: f.data.Deployment.AWS.Profile,
	})
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	f.resolver = secretsx.NewResolver(clients.SSM(), f.data.Deployment.Env)
	f.awsClients = clients
	return nil
}

// stackSecrets resolves every secret a stack declares, generating a random
// 32-byte value on first use. Keys are the reference leaf names. tenantID,
// when non-empty, substitutes "{tenant}" in each ref — used by
// deployLocalHost so a per-tenant local_hosts stack (e.g. `gitea`) gets its
// own secret per tenant rather than sharing one across every tenant's
// instance; cloud/shared stacks pass "" and get the ref as-is.
func (f *fleet) stackSecrets(ctx context.Context, stack model.Stack, tenantID string) (map[string]string, error) {
	if len(stack.Secrets) == 0 {
		return nil, nil
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(stack.Secrets))
	for _, ref := range stack.Secrets {
		if tenantID != "" {
			ref = strings.ReplaceAll(ref, "{tenant}", tenantID)
		}
		value, err := f.resolver.EnsureRandom(ctx, ref, 32)
		if err != nil {
			return nil, fmt.Errorf("resolve secret %s: %w", ref, err)
		}
		out[secretsx.Leaf(ref)] = value
	}
	return out, nil
}

func (a *App) openFleet(ctx context.Context, printer ui.Printer, globals *globalOptions) (*fleet, error) {
	root, err := workspace.Discover(defaultStartDirectory(), globals.Workspace)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return nil, &ExitError{Code: 2, Err: fmt.Errorf("%w; pass --workspace", err)}
		}
		return nil, err
	}
	data, report, err := workspace.Validate(ctx, root, workspace.ValidateOptions{
		RunningVersion: fallback(a.build.Version, "dev"),
		AllowDowngrade: globals.AllowDowngrade,
	})
	if err != nil {
		return nil, err
	}
	if !report.Valid {
		return nil, &ExitError{Code: 1, Err: fmt.Errorf("workspace is invalid; run mksrv validate"), Printed: true}
	}
	outputs, err := infra.LoadOutputs(data.Root)
	if err != nil {
		return nil, &ExitError{Code: 2, Err: err}
	}
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		return nil, err
	}
	cacheDir, err := engine.Extract(ctx, fallback(a.build.Version, "dev"))
	if err != nil {
		return nil, err
	}

	f := &fleet{
		data:       data,
		catalog:    catalog,
		knownHosts: filepath.Join(data.Root, ".mksrv", "known_hosts"),
		stacksRoot: filepath.Join(cacheDir, "stacks"),
		byName:     map[string]hostTarget{},
		outputs:    outputs,
	}
	f.loadMeshIPs()
	f.loadPostgres()
	f.loadOpenBao()
	names := make([]string, 0, len(data.Deployment.Hosts))
	for name := range data.Deployment.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		host := data.Deployment.Hosts[name]
		out, ok := outputs.Hosts[name]
		if !ok {
			return nil, &ExitError{Code: 2, Err: fmt.Errorf("no infra output for host %q; re-run apply --infra-only", name)}
		}
		user := host.SSHUser
		if user == "" {
			user = "rocky"
		}
		port := host.SSHPort
		if port == 0 {
			port = 22
		}
		ht := hostTarget{
			Name:   name,
			Host:   host,
			Target: sshx.Target{Host: out.ManagementIP, Port: port, User: user},
		}
		f.targets = append(f.targets, ht)
		f.byName[name] = ht
	}
	f.wireBastion()
	f.addLocalHosts()
	return f, nil
}

// addLocalHosts appends one hostTarget per tenant local_hosts entry (ADR
// 0034), across every declared tenant, sorted by tenant id then host name.
// These never go through wireBastion/SSH: Host is synthesized with just the
// entry's own Stacks so orderedStacks/renderContext's existing
// Host.Stacks-based logic works unchanged.
func (f *fleet) addLocalHosts() {
	tenantIDs := sortedTenantIDs(f.data.Tenants)
	for _, tenantID := range tenantIDs {
		tenant := f.data.Tenants[tenantID]
		hostNames := make([]string, 0, len(tenant.LocalHosts))
		for name := range tenant.LocalHosts {
			hostNames = append(hostNames, name)
		}
		sort.Strings(hostNames)
		for _, name := range hostNames {
			entry := tenant.LocalHosts[name]
			ht := hostTarget{
				Name:      name,
				Host:      model.Host{Stacks: entry.Stacks},
				Local:     true,
				LocalHost: &entry,
				TenantID:  tenantID,
			}
			f.targets = append(f.targets, ht)
			f.byName[name] = ht
		}
	}
}

// wireBastion routes every private AWS host's SSH through the edge (ADR 0027).
// A private host's `management_ip` is a VPC address with no public route, so
// `openFleet` sets its `Target.Jump` to the `base` host, which the CLI always
// bootstraps first. A single-host fleet and `existing` hosts are untouched.
func (f *fleet) wireBastion() {
	var edge sshx.Target
	found := false
	for _, ht := range f.targets {
		if slices.Contains(ht.Host.Stacks, "base") && ht.Host.Provider != "existing" {
			edge = ht.Target
			found = true
			break
		}
	}
	if !found || len(f.targets) < 2 {
		return
	}
	for i := range f.targets {
		ht := &f.targets[i]
		if ht.Host.Provider == "existing" || slices.Contains(ht.Host.Stacks, "base") {
			continue
		}
		jump := edge
		ht.Target.Jump = &jump
		f.byName[ht.Name] = *ht
	}
}

// selected resolves the hosts a command should act on. With no names, it
// returns every cloud/SSH-reachable fleet host (base first) — never a
// tenant's local_hosts entries (ADR 0034): those only ever run when named
// explicitly, so an unqualified `mksrv deploy`/`mksrv host trust` from the
// wrong machine can't trip a local host's safety guard. Naming one
// explicitly resolves it through f.byName same as any other host.
func (f *fleet) selected(names []string) ([]hostTarget, error) {
	if len(names) == 0 {
		var cloud []hostTarget
		for _, ht := range f.targets {
			if !ht.Local {
				cloud = append(cloud, ht)
			}
		}
		return baseFirst(cloud), nil
	}
	var out []hostTarget
	for _, name := range names {
		ht, ok := f.byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown host %q", name)
		}
		out = append(out, ht)
	}
	return baseFirst(out), nil
}

// baseFirst reorders hosts so the `base` host (the edge / SSH bastion) comes
// first — it must be reachable and trusted before the CLI can jump to the
// private hosts behind it (ADR 0027). Order within each group is preserved.
func baseFirst(hosts []hostTarget) []hostTarget {
	out := make([]hostTarget, 0, len(hosts))
	for _, ht := range hosts {
		if slices.Contains(ht.Host.Stacks, "base") {
			out = append(out, ht)
		}
	}
	for _, ht := range hosts {
		if !slices.Contains(ht.Host.Stacks, "base") {
			out = append(out, ht)
		}
	}
	return out
}

func (a *App) newHostCommand(opts *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "host", Short: "Manage fleet host trust and connectivity"}
	trust := &cobra.Command{
		Use:   "trust [HOST...]",
		Short: "Record fleet host SSH keys in the workspace known_hosts",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runHostTrust(cmd.Context(), a.printer(opts), opts, args)
		},
	}
	cmd.AddCommand(trust)
	cmd.AddCommand(&cobra.Command{
		Use:   "migrate-volume HOST STACK [NAME...]",
		Short: "Copy a stack's named-podman-volume data onto its dedicated EBS volume",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runMigrateVolume(cmd.Context(), a.printer(opts), opts, args[0], args[1], args[2:])
		},
	})
	return cmd
}

func (a *App) runHostTrust(ctx context.Context, printer ui.Printer, globals *globalOptions, args []string) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	hosts, err := f.selected(args)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	for _, ht := range hosts {
		if ht.Local {
			printer.Info("%s: local host, no SSH key to trust (ADR 0034)", ht.Name)
			continue
		}
		res, err := sshx.Trust(ctx, f.knownHosts, ht.Target)
		if err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("trust %s: %w", ht.Name, err), Printed: false}
		}
		if res.Added {
			printer.Success("%s (%s) %s", ht.Name, ht.Target.Host, res.Fingerprint)
		} else {
			printer.Info("%s already trusted (%s)", ht.Name, res.Fingerprint)
		}
	}
	return nil
}

func (a *App) newBootstrapCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "bootstrap [HOST...]",
		Short: "Prepare Rocky Linux hosts (SELinux, firewalld, Podman, data volume)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runBootstrap(cmd.Context(), a.printer(opts), opts, args)
		},
	}
}

func (a *App) runBootstrap(ctx context.Context, printer ui.Printer, globals *globalOptions, args []string) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	hosts, err := f.selected(args)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	for _, ht := range hosts {
		if ht.Local {
			printer.Info("%s: local host, nothing to bootstrap (ADR 0034)", ht.Name)
			continue
		}
		client, err := sshx.Dial(ctx, ht.Target, f.knownHosts)
		if err != nil {
			return dialError(ht.Name, err)
		}
		printer.Info("%s: bootstrapping", ht.Name)
		res, err := deploy.Bootstrap(ctx, client, f.bootstrapParams(ht))
		_ = client.Close()
		if err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("%s: %w", ht.Name, err), Printed: false}
		}
		printer.Success("%s: %s", ht.Name, lastLine(res.Stdout))
	}
	return nil
}

func (a *App) newDeployCommand(opts *globalOptions) *cobra.Command {
	var stackNames []string
	cmd := &cobra.Command{
		Use:   "deploy [HOST...]",
		Short: "Render and deploy stacks to their hosts",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runDeploy(cmd.Context(), a.printer(opts), opts, args, stackNames)
		},
	}
	cmd.Flags().StringSliceVar(&stackNames, "stack", nil, "Limit to these stacks (default: every stack on the selected hosts)")
	return cmd
}

func (a *App) runDeploy(ctx context.Context, printer ui.Printer, globals *globalOptions, args, stackNames []string) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	hosts, err := f.selected(args)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	limit := map[string]bool{}
	for _, name := range stackNames {
		limit[name] = true
	}

	for _, ht := range hosts {
		if ht.Local {
			if err := f.deployLocalHost(ctx, printer, ht, limit); err != nil {
				return err
			}
			continue
		}
		client, err := sshx.Dial(ctx, ht.Target, f.knownHosts)
		if err != nil {
			return dialError(ht.Name, err)
		}
		err = f.deployHost(ctx, printer, ht, client, limit)
		_ = client.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// localHostGuardName is the identity deployLocalHost/the local mesh join
// check this machine against — MKSRV_LOCAL_HOST when set (for a machine
// whose real hostname differs, or for testing), otherwise os.Hostname().
func localHostGuardName() (string, error) {
	if v := strings.TrimSpace(os.Getenv("MKSRV_LOCAL_HOST")); v != "" {
		return v, nil
	}
	name, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("determine local hostname: %w", err)
	}
	return name, nil
}

// checkLocalHostGuard refuses to act on ht unless this machine's own
// identity (localHostGuardName) matches ht.Name — ADR 0034's hard safety
// requirement: never silently no-op, or act on the wrong machine, when a
// tenant's local_hosts entry is named on a machine that isn't it.
func checkLocalHostGuard(ht hostTarget) error {
	name, err := localHostGuardName()
	if err != nil {
		return err
	}
	if !strings.EqualFold(name, ht.Name) {
		return fmt.Errorf("refusing to act on local host %q from %q (set MKSRV_LOCAL_HOST if this machine's hostname is wrong)", ht.Name, name)
	}
	return nil
}

// deployLocalHost renders and deploys a tenant's local_hosts entry
// in-process on this machine (ADR 0034) — no SSH, no bootstrap.
func (f *fleet) deployLocalHost(ctx context.Context, printer ui.Printer, ht hostTarget, limit map[string]bool) error {
	if err := checkLocalHostGuard(ht); err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	runner := deploy.NewLocalRunner()
	deployed, _ := deploy.DeployedStacks(ctx, runner)
	rctx, err := f.renderLocalContext(ht)
	if err != nil {
		return &ExitError{Code: 2, Err: fmt.Errorf("%s: %w", ht.Name, err)}
	}
	rctx.Deployed = deployed

	for _, stackName := range orderedStacks(f.catalog, ht.Host.Stacks) {
		if len(limit) > 0 && !limit[stackName] {
			continue
		}
		stack := f.catalog[stackName]
		if len(stack.Templates) == 0 {
			continue
		}
		secretValues, err := f.stackSecrets(ctx, stack, ht.TenantID)
		if err != nil {
			return &ExitError{Code: 2, Err: fmt.Errorf("%s/%s: %w", ht.Name, stackName, err)}
		}

		instances := []string{""} // one pass, InstanceName empty, for every non-multi-instance stack
		if stackName == "meshnode" && ht.LocalHost.Meshnode != nil {
			instances = make([]string, 0, len(ht.LocalHost.Meshnode.Instances))
			for _, inst := range ht.LocalHost.Meshnode.Instances {
				instances = append(instances, inst.Name)
			}
		}
		for _, instance := range instances {
			instCtx := rctx
			instCtx.InstanceName = instance
			label := stackName
			if instance != "" {
				label = fmt.Sprintf("%s[%s]", stackName, instance)
			}
			printer.Info("%s: deploying %s", ht.Name, label)
			res, err := deploy.DeployStack(ctx, runner, deploy.Options{
				StacksRoot: f.stacksRoot,
				Stack:      stack,
				Context:    instCtx,
				Secrets:    secretValues,
			})
			if err != nil {
				return &ExitError{Code: 1, Err: fmt.Errorf("%s/%s: %w", ht.Name, label, err), Printed: false}
			}
			printer.Success("%s/%s: %d changed, %d unchanged, health %v", ht.Name, label, len(res.Changed), res.Unchanged, res.HealthOK)
		}
		if !slices.Contains(rctx.Deployed, stackName) {
			rctx.Deployed = append(rctx.Deployed, stackName)
		}
	}
	return nil
}

// renderLocalContext is renderContext's sibling for a tenant local_hosts
// entry (ADR 0034): no infra.Outputs lookup (none exists off the cloud
// fleet), so the fleet-cross-host fields (Peers/StackHosts/StackMembers/
// Fleet/OperatorFQDNs) are left empty — they're M23 cross-host-scraping
// concerns that don't apply here.
func (f *fleet) renderLocalContext(ht hostTarget) (render.Context, error) {
	tenant, ok := f.data.Tenants[ht.TenantID]
	if !ok {
		return render.Context{}, fmt.Errorf("owning tenant %q not found", ht.TenantID)
	}
	dep := f.data.Deployment
	images := map[string]string{}
	for _, stackName := range ht.Host.Stacks {
		for _, app := range f.catalog[stackName].Apps {
			images[app.Name] = app.Image
		}
	}
	return render.Context{
		Env:       dep.Env,
		Region:    dep.AWS.Region,
		Timezone:  dep.Timezone,
		ACMEEmail: dep.Identity.ACMEEmail,
		Host: render.HostView{
			Name:   ht.Name,
			Role:   "local",
			Stacks: ht.Host.Stacks,
		},
		Endpoints: render.Endpoints{
			Keycloak:   dep.Identity.KeycloakDomain,
			Headscale:  dep.Identity.HeadscaleDomain,
			ConfigD:    "cfg." + dep.DNS.RootDomain,
			RootDomain: dep.DNS.RootDomain,
		},
		Images:        images,
		TenantIDs:     sortedTenantIDs(f.data.Tenants),
		Tenant:        &tenant,
		LocalHost:     ht.LocalHost,
		LocalHostName: ht.Name,
	}, nil
}

// deployHost renders and deploys every templated stack the host carries (or the
// limit subset), in dependency order, resolving each stack's secrets first.
func (f *fleet) deployHost(ctx context.Context, printer ui.Printer, ht hostTarget, client *sshx.Client, limit map[string]bool) error {
	deployed, _ := deploy.DeployedStacks(ctx, client)
	rctx := f.renderContext(ht)
	rctx.Deployed = deployed

	// A non-edge host's stacks may still contribute Caddy vhost fragments, which
	// belong on the edge.
	var edgeClient *sshx.Client
	if !slices.Contains(ht.Host.Stacks, "base") {
		if edge := f.identityHost(); edge != nil {
			if ec, err := sshx.Dial(ctx, edge.Target, f.knownHosts); err == nil {
				edgeClient = ec
				defer edgeClient.Close()
			}
		}
	}

	for _, stackName := range orderedStacks(f.catalog, ht.Host.Stacks) {
		if len(limit) > 0 && !limit[stackName] {
			continue
		}
		stack := f.catalog[stackName]
		if len(stack.Templates) == 0 {
			continue
		}
		secretValues, err := f.stackSecrets(ctx, stack, "")
		if err != nil {
			return &ExitError{Code: 2, Err: fmt.Errorf("%s/%s: %w", ht.Name, stackName, err)}
		}
		if stackName == "identity" {
			// keycloak.container.tmpl mounts a themes/<id> directory per
			// declared tenant; podman does not create a missing bind-mount
			// source, and a missing one would fail the whole container (M24).
			// Guarantee every tenant's directory exists before the unit
			// (re)starts, regardless of whether `tenant apply` has run for it
			// yet.
			if err := ensureTenantThemeDirs(ctx, client, sortedTenantIDs(f.data.Tenants)); err != nil {
				return &ExitError{Code: 2, Err: fmt.Errorf("%s/identity: ensure theme dirs: %w", ht.Name, err)}
			}
		}
		printer.Info("%s: deploying %s", ht.Name, stackName)
		res, err := deploy.DeployStack(ctx, client, deploy.Options{
			StacksRoot: f.stacksRoot,
			Stack:      stack,
			Context:    rctx,
			Secrets:    secretValues,
			Edge:       edgeClient,
		})
		if err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("%s/%s: %w", ht.Name, stackName, err), Printed: false}
		}
		if !slices.Contains(rctx.Deployed, stackName) {
			rctx.Deployed = append(rctx.Deployed, stackName)
		}
		printer.Success("%s/%s: %d changed, %d unchanged, health %v", ht.Name, stackName, len(res.Changed), res.Unchanged, res.HealthOK)
	}
	return nil
}

// ensureTenantThemeDirs mkdir -p's every declared tenant's Keycloak login
// theme directory (M24). A no-op when there are no tenants yet.
func ensureTenantThemeDirs(ctx context.Context, client *sshx.Client, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := client.Run(ctx, themeDirsCommand(ids))
	return err
}

// themeDirsCommand builds the mkdir -p command ensureTenantThemeDirs runs.
// Split out so the command shape is unit-testable without an SSH double.
func themeDirsCommand(ids []string) string {
	var b strings.Builder
	b.WriteString("sudo mkdir -p")
	for _, id := range ids {
		fmt.Fprintf(&b, " /var/lib/mksrv/stacks/identity/themes/%s/login/resources/css", quoteArg(themeName(id)))
	}
	return b.String()
}

func (f *fleet) renderContext(ht hostTarget) render.Context {
	hostOut := f.outputs.Hosts[ht.Name]
	dep := f.data.Deployment
	role := "data"
	if slices.Contains(ht.Host.Stacks, "base") {
		role = "edge"
	}
	images := map[string]string{}
	for _, stackName := range ht.Host.Stacks {
		for _, app := range f.catalog[stackName].Apps {
			images[app.Name] = app.Image
		}
	}
	// Peers: other fleet hosts' private (VPC) IPs — same subnet, so the edge
	// reaches data-plane services directly without the mesh.
	peers := map[string]string{}
	for name, out := range f.outputs.Hosts {
		if name != ht.Name && out.PrivateIP != "" {
			peers[name] = out.PrivateIP
		}
	}
	// StackHosts/StackMembers: which fleet host(s) carry each stack. targets is
	// already sorted by host name, so StackMembers slices are too.
	stackHosts := map[string]string{}
	stackMembers := map[string][]render.Member{}
	fleet := make([]render.Member, 0, len(f.targets))
	for _, t := range f.targets {
		ip := f.outputs.Hosts[t.Name].PrivateIP
		if ip == "" {
			continue
		}
		memberRole := "data"
		if slices.Contains(t.Host.Stacks, "base") {
			memberRole = "edge"
		}
		member := render.Member{Name: t.Name, PrivateIP: ip, TailnetIP: f.meshIPs[t.Name], Role: memberRole, Stacks: t.Host.Stacks}
		fleet = append(fleet, member)
		for _, s := range t.Host.Stacks {
			stackHosts[s] = ip
			stackMembers[s] = append(stackMembers[s], member)
		}
	}
	// OperatorFQDNs: every shared operator endpoint plus each tenant's
	// PostgREST vhost, fronted by the edge — mirrors infra/root/main.tf's
	// local.operator_fqdns so blackbox probes exactly what DNS actually
	// points at (M23 phase 3).
	operatorFQDNs := []string{
		dep.Identity.KeycloakDomain,
		dep.Identity.HeadscaleDomain,
		"cfg." + dep.DNS.RootDomain,
		"grafana." + dep.DNS.RootDomain,
		"pgadmin." + dep.DNS.RootDomain,
	}
	for _, id := range sortedTenantIDs(f.data.Tenants) {
		if slices.Contains(f.data.Tenants[id].Stacks, "database") {
			operatorFQDNs = append(operatorFQDNs, id+".rest."+dep.DNS.RootDomain)
		}
	}

	return render.Context{
		Env:             dep.Env,
		Region:          dep.AWS.Region,
		Timezone:        dep.Timezone,
		ACMEEmail:       dep.Identity.ACMEEmail,
		OpenBaoKMSKeyID: hostOut.OpenBaoKMSKeyID,
		Host: render.HostView{
			Name:      ht.Name,
			Role:      role,
			PublicIP:  hostOut.PublicIP,
			PrivateIP: hostOut.PrivateIP,
			TailnetIP: f.meshIPs[ht.Name],
			MeshName:  fmt.Sprintf("%s-%s.%s.mksrv", dep.Env, ht.Name, dep.Env),
			Stacks:    ht.Host.Stacks,
		},
		Endpoints: render.Endpoints{
			Keycloak:   dep.Identity.KeycloakDomain,
			Headscale:  dep.Identity.HeadscaleDomain,
			ConfigD:    "cfg." + dep.DNS.RootDomain,
			RootDomain: dep.DNS.RootDomain,
		},
		Images:            images,
		Peers:             peers,
		StackHosts:        stackHosts,
		StackMembers:      stackMembers,
		Fleet:             fleet,
		OperatorFQDNs:     operatorFQDNs,
		TenantIDs:         sortedTenantIDs(f.data.Tenants),
		MailRelayOutbound: dep.Mail != nil && dep.Mail.RelayOutbound,
		Retention:         f.data.Deployment.Retention.Resolved(),
	}
}

// bootstrapParams derives the per-host bootstrap inputs: an adaptive swap size
// from the host's stack set and instance type, plus the EBS volume ids the
// bootstrap matches NVMe disks against.
func (f *fleet) bootstrapParams(ht hostTarget) deploy.BootstrapParams {
	itype := ht.Host.InstanceType
	if itype == "" {
		itype = "t4g.small"
	}
	out := f.outputs.Hosts[ht.Name]
	vols := make([]deploy.VolumeMount, 0, len(out.Volumes))
	for name := range out.Volumes {
		vols = append(vols, deploy.VolumeMount{Name: name, VolumeID: out.Volumes[name]})
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	isEdge := slices.Contains(ht.Host.Stacks, "base")
	return deploy.BootstrapParams{
		IsEdge:       isEdge,
		NAT:          isEdge && ht.Host.Provider != "existing" && len(f.targets) > 1,
		Timezone:     f.data.Deployment.Timezone,
		SwapMB:       model.SwapForStacks(ht.Host.Stacks, f.catalog, itype),
		DataVolumeID: out.DataVolumeID,
		Volumes:      vols,
	}
}

const meshFile = ".mksrv/mesh.json"

func (f *fleet) loadMeshIPs() {
	f.meshIPs = map[string]string{}
	blob, err := os.ReadFile(filepath.Join(f.data.Root, filepath.FromSlash(meshFile)))
	if err != nil {
		return
	}
	_ = json.Unmarshal(blob, &f.meshIPs)
}

func (f *fleet) writeMeshIPs(ips map[string]string) error {
	blob, err := json.MarshalIndent(ips, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.data.Root, filepath.FromSlash(meshFile)), append(blob, '\n'), 0o600)
}

// runFleetApply bootstraps and deploys every host, base host first.
func (a *App) runFleetApply(ctx context.Context, printer ui.Printer, globals *globalOptions, trustHosts bool) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}

	// Local hosts (ADR 0034) never bootstrap or deploy as part of the
	// fleet-wide apply — they only ever run explicitly, via `mksrv deploy
	// <name>`, on the machine they actually are.
	var cloud []hostTarget
	for _, ht := range f.targets {
		if !ht.Local {
			cloud = append(cloud, ht)
		}
	}
	ordered := baseFirst(cloud)

	for _, ht := range ordered {
		if trustHosts {
			if _, err := sshx.Trust(ctx, f.knownHosts, ht.Target); err != nil {
				return &ExitError{Code: 1, Err: fmt.Errorf("trust %s: %w", ht.Name, err)}
			}
		}
		client, err := sshx.Dial(ctx, ht.Target, f.knownHosts)
		if err != nil {
			return dialError(ht.Name, err)
		}

		printer.Info("%s: bootstrapping", ht.Name)
		if _, err := deploy.Bootstrap(ctx, client, f.bootstrapParams(ht)); err != nil {
			_ = client.Close()
			return &ExitError{Code: 1, Err: fmt.Errorf("%s: %w", ht.Name, err), Printed: false}
		}

		err = f.deployHost(ctx, printer, ht, client, nil)
		_ = client.Close()
		if err != nil {
			return err
		}
	}
	printer.Success("fleet applied")
	return nil
}

const fleetMeshUser = "mksrv-fleet"

func (a *App) newMeshCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "mesh",
		Short: "Reconcile Headscale users and join non-edge hosts to the tailnet",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runMesh(cmd.Context(), a.printer(opts), opts)
		},
	}
}

func (a *App) runMesh(ctx context.Context, printer ui.Printer, globals *globalOptions) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	var edge *hostTarget
	for i := range f.targets {
		if slices.Contains(f.targets[i].Host.Stacks, "identity") {
			edge = &f.targets[i]
			break
		}
	}
	if edge == nil {
		return &ExitError{Code: 2, Err: fmt.Errorf("no host carries identity; deploy it first")}
	}

	edgeClient, err := sshx.Dial(ctx, edge.Target, f.knownHosts)
	if err != nil {
		return dialError(edge.Name, err)
	}
	defer edgeClient.Close()
	hs := headscale.New(edgeClient)

	// One Headscale user per tenant (for Cloud-IT VPN clients) plus a fleet user.
	users := []string{fleetMeshUser}
	tenantIDs := make([]string, 0, len(f.data.Tenants))
	for id := range f.data.Tenants {
		tenantIDs = append(tenantIDs, id)
	}
	sort.Strings(tenantIDs)
	users = append(users, tenantIDs...)
	for _, name := range users {
		if _, err := hs.EnsureUser(ctx, name); err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("headscale user %s: %w", name, err)}
		}
		printer.Info("headscale user %s ready", name)
	}

	fleetID, err := hs.EnsureUser(ctx, fleetMeshUser)
	if err != nil {
		return &ExitError{Code: 1, Err: err}
	}

	loginServer := "https://" + f.data.Deployment.Identity.HeadscaleDomain
	meshIPs := map[string]string{}
	for _, ht := range f.targets {
		if ht.Local {
			continue // joined below, in-process, not over SSH
		}
		key, err := hs.PreAuthKey(ctx, fleetID, time.Hour, false)
		if err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("preauth key for %s: %w", ht.Name, err)}
		}
		client, err := sshx.Dial(ctx, ht.Target, f.knownHosts)
		if err != nil {
			return dialError(ht.Name, err)
		}
		printer.Info("%s: joining tailnet", ht.Name)
		ip, err := deploy.JoinMesh(ctx, client, deploy.MeshParams{
			LoginServer: loginServer,
			AuthKey:     key,
			Hostname:    fmt.Sprintf("%s-%s", f.data.Deployment.Env, ht.Name),
		})
		_ = client.Close()
		if err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("%s: %w", ht.Name, err), Printed: false}
		}
		meshIPs[ht.Name] = ip
		printer.Success("%s joined tailnet as %s", ht.Name, ip)
	}
	if err := f.writeMeshIPs(meshIPs); err != nil {
		printer.Warn("could not write %s: %v", meshFile, err)
	}
	f.meshIPs = meshIPs
	return f.joinLocalMeshNodes(ctx, printer, hs)
}

// joinLocalMeshNodes joins any meshnode instance declared on THIS machine
// (ADR 0034) to the tailnet — one-time per instance, via `systemctl
// set-environment TS_AUTHKEY=...` + restart rather than a podman Secret (see
// stacks/meshnode/templates/mesh.container.tmpl). `mksrv mesh` has no single
// named target the way `mksrv deploy <name>` does, so entries that don't
// match this machine's hostname guard are silently skipped (not a mistake —
// just not this instance's turn) rather than failing the whole command.
func (f *fleet) joinLocalMeshNodes(ctx context.Context, printer ui.Printer, hs *headscale.Client) error {
	guardName, err := localHostGuardName()
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	runner := deploy.NewLocalRunner()
	for _, ht := range f.targets {
		if !ht.Local || !strings.EqualFold(ht.Name, guardName) || ht.LocalHost.Meshnode == nil {
			continue
		}
		tenantUserID, err := hs.EnsureUser(ctx, ht.TenantID)
		if err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("headscale user %s: %w", ht.TenantID, err)}
		}
		for _, inst := range ht.LocalHost.Meshnode.Instances {
			unit := "mesh-" + inst.Name + ".service"
			if res, err := runner.Run(ctx, "systemctl is-active --quiet "+quoteArg(unit)); err == nil && res.ExitCode == 0 {
				printer.Info("%s/%s: already joined", ht.Name, inst.Name)
				continue
			}
			key, err := hs.PreAuthKey(ctx, tenantUserID, time.Hour, false)
			if err != nil {
				return &ExitError{Code: 1, Err: fmt.Errorf("preauth key for %s/%s: %w", ht.Name, inst.Name, err)}
			}
			if _, err := runner.Run(ctx, "sudo systemctl set-environment TS_AUTHKEY="+quoteArg(key)); err != nil {
				return &ExitError{Code: 1, Err: fmt.Errorf("%s/%s: set-environment: %w", ht.Name, inst.Name, err)}
			}
			_, err = runner.Run(ctx, "sudo systemctl restart "+quoteArg(unit))
			// The key is single-use; don't leave it sitting in the manager's
			// global environment any longer than the restart needed it.
			_, _ = runner.Run(ctx, "sudo systemctl unset-environment TS_AUTHKEY")
			if err != nil {
				return &ExitError{Code: 1, Err: fmt.Errorf("%s/%s: restart: %w", ht.Name, inst.Name, err), Printed: false}
			}
			printer.Success("%s/%s: joined tailnet", ht.Name, inst.Name)
		}
	}
	return nil
}

func (a *App) newStatusCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report fleet host and stack health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runStatus(cmd.Context(), a.printer(opts), opts)
		},
	}
}

type hostStatus struct {
	Host         string   `json:"host"`
	Reachable    bool     `json:"reachable"`
	Bootstrapped bool     `json:"bootstrapped"`
	FailedUnits  []string `json:"failed_units"`
	Detail       string   `json:"detail,omitempty"`
}

func (a *App) runStatus(ctx context.Context, printer ui.Printer, globals *globalOptions) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	statuses := make([]hostStatus, 0, len(f.targets))
	healthy := true
	for _, ht := range f.targets {
		if ht.Local {
			// No SSH session to check; local hosts aren't fleet status's concern.
			continue
		}
		st := hostStatus{Host: ht.Name}
		client, err := sshx.Dial(ctx, ht.Target, f.knownHosts)
		if err != nil {
			st.Detail = err.Error()
			healthy = false
			statuses = append(statuses, st)
			continue
		}
		st.Reachable = true
		if exists, _ := client.Exists(fmt.Sprintf("/var/lib/mksrv/.bootstrap-v%d", deploy.BootstrapVersion)); exists {
			st.Bootstrapped = true
		}
		if res, err := client.Run(ctx, "systemctl list-units --state=failed --no-legend --plain | awk '{print $1}'"); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					st.FailedUnits = append(st.FailedUnits, line)
				}
			}
		}
		_ = client.Close()
		if !st.Bootstrapped || len(st.FailedUnits) > 0 {
			healthy = false
		}
		statuses = append(statuses, st)
	}

	if printer.JSON {
		return printer.Encode(map[string]any{"healthy": healthy, "hosts": statuses})
	}
	for _, st := range statuses {
		switch {
		case !st.Reachable:
			printer.Error("%-8s unreachable: %s", st.Host, st.Detail)
		case !st.Bootstrapped:
			printer.Warn("%-8s reachable, not bootstrapped", st.Host)
		case len(st.FailedUnits) > 0:
			printer.Error("%-8s failed units: %s", st.Host, strings.Join(st.FailedUnits, ", "))
		default:
			printer.Success("%-8s healthy", st.Host)
		}
	}
	if !healthy {
		return &ExitError{Code: 1, Err: fmt.Errorf("fleet is not fully healthy"), Printed: true}
	}
	return nil
}

func dialError(host string, err error) error {
	if errors.Is(err, sshx.ErrUnknownHostKey) {
		return &ExitError{Code: 2, Err: fmt.Errorf("%s host key is not trusted; run: mksrv host trust %s", host, host)}
	}
	return &ExitError{Code: 1, Err: fmt.Errorf("connect %s: %w", host, err), Printed: false}
}

// orderedStacks returns the subset of assigned present on a host, in dependency
// order.
func orderedStacks(catalog map[string]model.Stack, assigned []string) []string {
	want := map[string]bool{}
	for _, name := range assigned {
		want[name] = true
	}
	var order []string
	seen := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if seen[name] || !want[name] {
			return
		}
		seen[name] = true
		for _, dep := range catalog[name].DependsOn {
			visit(dep)
		}
		order = append(order, name)
	}
	names := make([]string, 0, len(assigned))
	names = append(names, assigned...)
	sort.Strings(names)
	for _, name := range names {
		visit(name)
	}
	return order
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "ok"
	}
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}
