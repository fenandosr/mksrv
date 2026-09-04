// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"github.com/fenandosr/mksrv/internal/infra"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/workspace"
)

// TestRenderContextPopulatesFleet checks the fleet-wide roster (M23) a
// cross-host template like prometheus.yml ranges over: every host, sorted by
// name, with the right role and private IP.
func TestRenderContextPopulatesFleet(t *testing.T) {
	t.Parallel()
	f := &fleet{
		data: workspace.Data{Deployment: model.Deployment{
			Hosts: map[string]model.Host{
				"core1": {Provider: "aws", Stacks: []string{"postgres", "openbao"}},
				"edge":  {Provider: "aws", Stacks: []string{"base", "identity"}},
			},
		}},
		targets: []hostTarget{
			{Name: "core1", Host: model.Host{Stacks: []string{"postgres", "openbao"}}},
			{Name: "edge", Host: model.Host{Stacks: []string{"base", "identity"}}},
		},
		outputs: infra.Outputs{Hosts: map[string]infra.HostOutput{
			"core1": {PrivateIP: "10.20.0.21"},
			"edge":  {PrivateIP: "10.20.0.10", PublicIP: "203.0.113.10"},
		}},
	}
	ctx := f.renderContext(f.targets[0])
	if len(ctx.Fleet) != 2 {
		t.Fatalf("Fleet = %+v, want 2 members", ctx.Fleet)
	}
	if ctx.Fleet[0].Name != "core1" || ctx.Fleet[1].Name != "edge" {
		t.Fatalf("Fleet not sorted by name: %+v", ctx.Fleet)
	}
	if ctx.Fleet[0].Role != "data" || ctx.Fleet[0].PrivateIP != "10.20.0.21" {
		t.Fatalf("core1 member wrong: %+v", ctx.Fleet[0])
	}
	if ctx.Fleet[1].Role != "edge" || ctx.Fleet[1].PrivateIP != "10.20.0.10" {
		t.Fatalf("edge member wrong: %+v", ctx.Fleet[1])
	}
}

// TestRenderContextPopulatesOperatorFQDNs checks the blackbox probe list
// (M23 phase 3): the shared operator endpoints plus one rest FQDN per tenant
// that carries `database` — mirroring infra/root/main.tf's operator_fqdns.
func TestRenderContextPopulatesOperatorFQDNs(t *testing.T) {
	t.Parallel()
	f := &fleet{
		data: workspace.Data{
			Deployment: model.Deployment{
				DNS:      model.DNSConfig{RootDomain: "example.com"},
				Identity: model.IdentityConfig{KeycloakDomain: "auth.example.com", HeadscaleDomain: "vpn.example.com"},
				Hosts:    map[string]model.Host{"edge": {Provider: "aws", Stacks: []string{"base"}}},
			},
			Tenants: map[string]model.Tenant{
				"bitabit": {ID: "bitabit", Stacks: []string{"database", "cache"}},
				"hg":      {ID: "hg", Stacks: []string{"cache"}}, // no database -> no rest vhost
			},
		},
		targets: []hostTarget{{Name: "edge", Host: model.Host{Stacks: []string{"base"}}}},
		outputs: infra.Outputs{Hosts: map[string]infra.HostOutput{"edge": {PrivateIP: "10.20.0.10"}}},
	}
	ctx := f.renderContext(f.targets[0])
	want := []string{"auth.example.com", "vpn.example.com", "cfg.example.com", "grafana.example.com", "pgadmin.example.com", "bitabit.rest.example.com"}
	if len(ctx.OperatorFQDNs) != len(want) {
		t.Fatalf("OperatorFQDNs = %v, want %v", ctx.OperatorFQDNs, want)
	}
	for i, fqdn := range want {
		if ctx.OperatorFQDNs[i] != fqdn {
			t.Fatalf("OperatorFQDNs[%d] = %q, want %q (full: %v)", i, ctx.OperatorFQDNs[i], fqdn, ctx.OperatorFQDNs)
		}
	}
	// M24: every declared tenant, not just `database` consumers — Keycloak's
	// per-tenant theme mount needs the full roster.
	if wantIDs := []string{"bitabit", "hg"}; len(ctx.TenantIDs) != 2 || ctx.TenantIDs[0] != wantIDs[0] || ctx.TenantIDs[1] != wantIDs[1] {
		t.Fatalf("TenantIDs = %v, want %v", ctx.TenantIDs, wantIDs)
	}
}

func TestThemeDirsCommand(t *testing.T) {
	t.Parallel()
	if got := themeDirsCommand(nil); got != "sudo mkdir -p" {
		t.Fatalf("themeDirsCommand(nil) = %q", got)
	}
	got := themeDirsCommand([]string{"bitabit", "hg"})
	want := "sudo mkdir -p /var/lib/mksrv/stacks/identity/themes/'bitabit'/login/resources/css" +
		" /var/lib/mksrv/stacks/identity/themes/'hg'/login/resources/css"
	if got != want {
		t.Fatalf("themeDirsCommand() = %q, want %q", got, want)
	}
}
