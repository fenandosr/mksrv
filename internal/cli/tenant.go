// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fenandosr/mksrv/internal/configd"
	"github.com/fenandosr/mksrv/internal/headscale"
	"github.com/fenandosr/mksrv/internal/keycloak"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/render"

	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

const (
	vpnClientID     = "cloud-it-vpn-desktop"
	configdClientID = "configd"
)

// vpnRedirectURIs are the OIDC loopback callbacks the Cloud-IT VPN desktop app
// uses. The literal ports are the app's fixed candidates; the wildcards cover
// any future dynamic-port build.
var vpnRedirectURIs = []string{
	"http://127.0.0.1:47017/callback",
	"http://127.0.0.1:47018/callback",
	"http://127.0.0.1:47019/callback",
	"http://127.0.0.1:*/callback",
	"http://localhost:*/callback",
}

func (a *App) newTenantCommand(opts *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "tenant", Short: "Reconcile tenant realms, mesh users, and the VPN broker"}
	cmd.AddCommand(&cobra.Command{
		Use:   "apply [ID...]",
		Short: "Create/update realms, groups, OIDC clients, and configd",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runTenantApply(cmd.Context(), a.printer(opts), opts, args)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List tenants in the workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runTenantList(cmd.Context(), a.printer(opts), opts)
		},
	})
	return cmd
}

func (a *App) runTenantList(ctx context.Context, printer ui.Printer, globals *globalOptions) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	ids := sortedTenantIDs(f.data.Tenants)
	if printer.JSON {
		return printer.Encode(map[string]any{"tenants": ids})
	}
	for _, id := range ids {
		t := f.data.Tenants[id]
		printer.Info("%-10s %-24s realm=%s", id, t.BaseDomain, tenantRealm(t))
	}
	return nil
}

func (a *App) runTenantApply(ctx context.Context, printer ui.Printer, globals *globalOptions, args []string) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}
	dep := f.data.Deployment

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

	adminPass, err := f.resolver.Get(ctx, "/mksrv/{env}/identity/keycloak_admin_password")
	if err != nil {
		return &ExitError{Code: 2, Err: fmt.Errorf("keycloak admin password: %w (deploy identity first)", err)}
	}
	kc := keycloak.New("https://" + dep.Identity.KeycloakDomain)
	if err := kc.Login(ctx, "admin", adminPass); err != nil {
		return &ExitError{Code: 1, Err: err}
	}

	selected, err := f.selectedTenants(args)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	for _, id := range selected {
		tenant := f.data.Tenants[id]
		realm := tenantRealm(tenant)
		res, err := kc.EnsureRealm(ctx, keycloak.RealmSpec{
			Realm:       realm,
			DisplayName: tenant.DisplayName,
			Groups:      []string{"apps", "both"},
			Clients: []keycloak.ClientSpec{
				{ClientID: vpnClientID, Public: true, RedirectURIs: vpnRedirectURIs},
				{ClientID: configdClientID, Public: false, RedirectURIs: []string{}},
			},
		})
		if err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("realm %s: %w", realm, err)}
		}
		if _, err := hs.EnsureUser(ctx, id); err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("headscale user %s: %w", id, err)}
		}
		printer.Success("tenant %s: realm %s (created=%v, groups+%d, clients+%d)",
			id, realm, res.RealmCreated, len(res.GroupsCreated), len(res.ClientsCreated))
	}

	// Tenant-isolation ACL: tenants reach the fleet on service ports and their
	// own devices; not each other.
	policy := headscale.Policy(sortedTenantIDs(f.data.Tenants))
	const policyHost = "/var/lib/mksrv/stacks/identity/headscale/policy.hujson"
	if err := edgeClient.WriteFileSudo(ctx, policyHost, []byte(policy), 0o644); err != nil {
		return &ExitError{Code: 1, Err: fmt.Errorf("write headscale policy: %w", err)}
	}
	if err := hs.SetPolicyFile(ctx, "/etc/headscale/policy.hujson"); err != nil {
		return &ExitError{Code: 1, Err: err}
	}
	printer.Success("headscale ACL applied (%d tenants isolated)", len(f.data.Tenants))

	if err := f.provisionDatabases(ctx, printer, selected); err != nil {
		return &ExitError{Code: 1, Err: err}
	}

	pubPEM, err := a.reconcileConfigd(ctx, printer, f, hs, edgeClient, *edge)
	if err != nil {
		return err
	}
	printer.Info("configd signing public key (add to each tenant.json signingKeys[]):\n%s", pubPEM)
	return nil
}

// reconcileConfigd ensures the broker's signing key, Headscale API key, and
// tenant roster, pushes them to the edge, and restarts configd.
func (a *App) reconcileConfigd(ctx context.Context, printer ui.Printer, f *fleet, hs *headscale.Client, edgeClient *sshx.Client, edge hostTarget) (string, error) {
	dep := f.data.Deployment
	r := f.resolver

	kid, err := r.EnsureString(ctx, "/mksrv/{env}/identity/configd_signing_kid", "mksrv-"+dep.Env+"-1")
	if err != nil {
		return "", err
	}
	signingKey, err := r.EnsureRandom(ctx, "/mksrv/{env}/identity/configd_signing_key", 32)
	if err != nil {
		return "", err
	}
	apiKey, err := r.Get(ctx, "/mksrv/{env}/identity/configd_headscale_apikey")
	if err != nil {
		apiKey, err = hs.CreateAPIKey(ctx, 365*24*time.Hour)
		if err != nil {
			return "", fmt.Errorf("create headscale api key: %w", err)
		}
		if err := r.Put(ctx, "/mksrv/{env}/identity/configd_headscale_apikey", apiKey); err != nil {
			return "", err
		}
	}

	roster := configd.Config{}
	for _, id := range sortedTenantIDs(f.data.Tenants) {
		tenant := f.data.Tenants[id]
		roster.Tenants = append(roster.Tenants, configd.TenantEntry{
			Issuer:        fmt.Sprintf("https://%s/realms/%s", dep.Identity.KeycloakDomain, tenantRealm(tenant)),
			Tenant:        id,
			DisplayName:   tenant.DisplayName,
			Primary:       tenantPrimary(tenant),
			HeadscaleUser: id,
			ControlURL:    "https://" + dep.Identity.HeadscaleDomain,
			Forwards:      demoForwards(dep.Env),
			UpdateFeedURL: fmt.Sprintf("https://%s/appcast.json", tenant.BaseDomain),
			MinVersion:    "0.1.0",
		})
	}
	rosterJSON, err := json.Marshal(roster)
	if err != nil {
		return "", err
	}
	if err := r.Put(ctx, "/mksrv/{env}/identity/configd_tenants", string(rosterJSON)); err != nil {
		return "", err
	}

	secretValues := map[string]string{
		"configd_signing_kid":      kid,
		"configd_signing_key":      signingKey,
		"configd_headscale_apikey": apiKey,
		"configd_tenants":          string(rosterJSON),
	}
	for _, leaf := range sortedKeys(secretValues) {
		if _, err := edgeClient.RunInput(ctx,
			"sudo podman secret create --replace "+quoteArg("mksrv-identity-"+leaf)+" -",
			[]byte(secretValues[leaf]),
		); err != nil {
			return "", fmt.Errorf("push %s: %w", leaf, err)
		}
	}

	unit, err := render.One(
		filepath.Join(f.stacksRoot, "identity", "templates", "configd.container.tmpl"),
		f.renderContext(edge),
	)
	if err != nil {
		return "", err
	}
	if err := edgeClient.WriteFileSudo(ctx, "/etc/containers/systemd/mksrv-configd.container", unit, 0o644); err != nil {
		return "", err
	}
	if _, err := edgeClient.Run(ctx, "sudo systemctl daemon-reload"); err != nil {
		return "", err
	}
	if _, err := edgeClient.Run(ctx, "sudo systemctl restart mksrv-configd.service"); err != nil {
		return "", fmt.Errorf("restart configd: %w", err)
	}
	_, _ = edgeClient.Run(ctx, "sudo podman network reload --all")
	if _, err := edgeClient.Run(ctx, "curl -fsS -m 10 --retry 12 --retry-delay 3 --retry-all-errors http://127.0.0.1:8090/healthz -o /dev/null"); err != nil {
		return "", fmt.Errorf("configd did not become healthy: %w", err)
	}
	printer.Success("configd healthy with %d tenants", len(roster.Tenants))

	keyBytes, err := configd.ParsePrivateKey([]byte(signingKey))
	if err != nil {
		return "", err
	}
	signer, err := configd.NewSigner(kid, keyBytes)
	if err != nil {
		return "", err
	}
	return configd.PublicKeyPEM(signer.PublicKey())
}

// demoForwards is a placeholder forward set until the data-plane stacks land.
// The first forward targets the edge health endpoint, which is reachable over
// the tailnet as soon as a client joins, so the desktop app can exercise the
// full tunnel path before database/monitor exist.
func demoForwards(env string) []configd.Forward {
	return []configd.Forward{
		{
			ID:           "edge-health",
			Label:        "Edge health",
			Type:         "http",
			Listen:       configd.Listen{Host: "127.0.0.1", Port: 0},
			PortStrategy: "auto",
			Target:       fmt.Sprintf("%s-edge.%s.mksrv:80", env, env),
			OpenAction:   configd.OpenAction{Kind: "browser", Path: "/healthz"},
			HealthCheck:  configd.HealthCheck{Kind: "http", Path: "/healthz", IntervalSec: 30},
			MaxConns:     8,
		},
		{
			ID:           "database",
			Label:        "PostgreSQL",
			Type:         "tcp",
			Listen:       configd.Listen{Host: "127.0.0.1", Port: 0},
			PortStrategy: "auto",
			Target:       fmt.Sprintf("%s-data.%s.mksrv:5432", env, env),
			OpenAction:   configd.OpenAction{Kind: "none"},
			HealthCheck:  configd.HealthCheck{Kind: "tcp", IntervalSec: 30},
			MaxConns:     16,
		},
	}
}

func (f *fleet) identityHost() *hostTarget {
	for i := range f.targets {
		if slices.Contains(f.targets[i].Host.Stacks, "identity") {
			return &f.targets[i]
		}
	}
	return nil
}

func (f *fleet) selectedTenants(args []string) ([]string, error) {
	all := sortedTenantIDs(f.data.Tenants)
	if len(args) == 0 {
		return all, nil
	}
	for _, id := range args {
		if _, ok := f.data.Tenants[id]; !ok {
			return nil, fmt.Errorf("unknown tenant %q", id)
		}
	}
	return args, nil
}

func sortedTenantIDs(tenants map[string]model.Tenant) []string {
	ids := make([]string, 0, len(tenants))
	for id := range tenants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func tenantRealm(t model.Tenant) string {
	if t.Keycloak.Realm != "" {
		return t.Keycloak.Realm
	}
	return t.ID
}

func tenantPrimary(t model.Tenant) string {
	if t.Branding.Primary != "" {
		return t.Branding.Primary
	}
	return "#0C6D77"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func quoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
