// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/schema"
	"github.com/fenandosr/mksrv/internal/workspace"
)

// The live fleet's host/stack layout, with the real catalog: the priority of every volume is
// what the snapshot policy (ADR 0046) acts on, so it is pinned here.
func tierFleet(t *testing.T) workspace.Data {
	t.Helper()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatal(err)
	}
	return workspace.Data{
		Catalog: catalog,
		Deployment: model.Deployment{Hosts: map[string]model.Host{
			"edge":  {Provider: "aws", Stacks: []string{"base", "identity", "mail"}},
			"core1": {Provider: "aws", Stacks: []string{"postgres", "openbao"}},
			"appd":  {Provider: "aws", Stacks: []string{"database", "cache", "monitor", "logs", "backup", "pgproxy"}},
			"local": {Provider: "existing", Stacks: []string{"database"}},
		}},
	}
}

func TestMaxPriority(t *testing.T) {
	t.Parallel()
	cases := []struct{ a, b, want string }{
		{"", "", model.PriorityStandard},
		{model.PriorityLow, model.PriorityStandard, model.PriorityStandard},
		{model.PriorityStandard, model.PriorityCritical, model.PriorityCritical},
		{model.PriorityCritical, model.PriorityLow, model.PriorityCritical},
		{model.PriorityLow, "", model.PriorityStandard}, // an unset priority is standard, not low
	}
	for _, c := range cases {
		if got := model.MaxPriority(c.a, c.b); got != c.want {
			t.Errorf("MaxPriority(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestHostDataTiersFollowTheStacksOfEachHost(t *testing.T) {
	t.Parallel()
	got := hostDataTiers(tierFleet(t))
	want := map[string]string{
		"edge":  model.PriorityCritical, // identity (every tenant's users, Headscale) and mail
		"core1": model.PriorityStandard, // their data sits on dedicated volumes, not on this one
		"appd":  model.PriorityStandard,
	}
	for host, tier := range want {
		if got[host] != tier {
			t.Errorf("data tier of %s = %q, want %q", host, got[host], tier)
		}
	}
	if _, ok := got["local"]; ok {
		t.Error("a host the fleet does not provision has no EBS volume to classify")
	}
}

func TestDedicatedVolumesCarryTheirPriority(t *testing.T) {
	t.Parallel()
	data := tierFleet(t)
	tiers := map[string]string{}
	for host, vols := range hostVolumes(data) {
		for _, v := range vols {
			tiers[host+"/"+v["name"].(string)] = v["tier"].(string)
		}
	}
	want := map[string]string{
		"edge/maildata": model.PriorityCritical,
		"core1/pgdata":  model.PriorityCritical,
		"core1/baoraft": model.PriorityCritical,
		"core1/raft":    model.PriorityStandard,
		"appd/tsdb":     model.PriorityLow,
		"appd/chunks":   model.PriorityLow,
	}
	for vol, tier := range want {
		if tiers[vol] != tier {
			t.Errorf("%s: tier = %q, want %q (all: %v)", vol, tiers[vol], tier, tiers)
		}
	}
	if len(tiers) != len(want) {
		t.Errorf("a dedicated volume is not classified in this test: %v", tiers)
	}
}
