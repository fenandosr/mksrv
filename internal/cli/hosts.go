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
	meshIPs    map[string]string
	postgres   postgresCluster
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
	return nil
}

// stackSecrets resolves every secret a stack declares, generating a random
// 32-byte value on first use. Keys are the reference leaf names.
func (f *fleet) stackSecrets(ctx context.Context, stack model.Stack) (map[string]string, error) {
	if len(stack.Secrets) == 0 {
		return nil, nil
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(stack.Secrets))
	for _, ref := range stack.Secrets {
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
	return f, nil
}

func (f *fleet) selected(names []string) ([]hostTarget, error) {
	if len(names) == 0 {
		return f.targets, nil
	}
	var out []hostTarget
	for _, name := range names {
		ht, ok := f.byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown host %q", name)
		}
		out = append(out, ht)
	}
	return out, nil
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
	timezone := f.data.Deployment.Timezone
	for _, ht := range hosts {
		client, err := sshx.Dial(ctx, ht.Target, f.knownHosts)
		if err != nil {
			return dialError(ht.Name, err)
		}
		params := deploy.BootstrapParams{
			IsEdge:   slices.Contains(ht.Host.Stacks, "base"),
			Timezone: timezone,
		}
		printer.Info("%s: bootstrapping", ht.Name)
		res, err := deploy.Bootstrap(ctx, client, params)
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
		secretValues, err := f.stackSecrets(ctx, stack)
		if err != nil {
			return &ExitError{Code: 2, Err: fmt.Errorf("%s/%s: %w", ht.Name, stackName, err)}
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
	for _, t := range f.targets {
		ip := f.outputs.Hosts[t.Name].PrivateIP
		if ip == "" {
			continue
		}
		member := render.Member{Name: t.Name, PrivateIP: ip, TailnetIP: f.meshIPs[t.Name]}
		for _, s := range t.Host.Stacks {
			stackHosts[s] = ip
			stackMembers[s] = append(stackMembers[s], member)
		}
	}
	return render.Context{
		Env:       dep.Env,
		Timezone:  dep.Timezone,
		ACMEEmail: dep.Identity.ACMEEmail,
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
		Images:       images,
		Peers:        peers,
		StackHosts:   stackHosts,
		StackMembers: stackMembers,
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

	ordered := make([]hostTarget, 0, len(f.targets))
	for _, ht := range f.targets {
		if slices.Contains(ht.Host.Stacks, "base") {
			ordered = append(ordered, ht)
		}
	}
	for _, ht := range f.targets {
		if !slices.Contains(ht.Host.Stacks, "base") {
			ordered = append(ordered, ht)
		}
	}

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
		if _, err := deploy.Bootstrap(ctx, client, deploy.BootstrapParams{
			IsEdge:   slices.Contains(ht.Host.Stacks, "base"),
			Timezone: f.data.Deployment.Timezone,
		}); err != nil {
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
