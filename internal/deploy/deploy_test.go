// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"strings"
	"testing"
)

func TestRenderBootstrapEdge(t *testing.T) {
	t.Parallel()
	script, err := RenderBootstrap(BootstrapParams{IsEdge: true, Timezone: "America/Mexico_City"})
	if err != nil {
		t.Fatalf("RenderBootstrap() error = %v", err)
	}
	for _, want := range []string{
		"IS_EDGE=1",
		`TIMEZONE="America/Mexico_City"`,
		"SELINUX=enforcing",
		"--add-service=http",
		`GRAPHROOT="$MARKER_DIR/containers"`,
		"semanage fcontext -a -e /var/lib/containers/storage",
		".bootstrap-v7",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bootstrap script missing %q", want)
		}
	}
}

func TestRenderBootstrapDataHostHasNoWebPorts(t *testing.T) {
	t.Parallel()
	script, err := RenderBootstrap(BootstrapParams{IsEdge: false})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "IS_EDGE=1") {
		t.Fatal("data host should not be edge")
	}
}

func TestQuadletUnits(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{
		"/etc/containers/systemd/mksrv-caddy.container": nil,
		"/etc/containers/systemd/mksrv-base.network":    nil,
		"/var/lib/mksrv/stacks/base/Caddyfile":          nil,
	}
	units := quadletUnits(files)
	if len(units) != 1 || units[0] != "mksrv-caddy.service" {
		t.Fatalf("quadletUnits() = %v", units)
	}
}
