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
