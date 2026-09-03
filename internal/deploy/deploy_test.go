// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"strings"
	"testing"
)

func TestRenderBootstrapEdge(t *testing.T) {
	t.Parallel()
	script, err := RenderBootstrap(BootstrapParams{
		IsEdge:       true,
		Timezone:     "America/Mexico_City",
		SwapMB:       1024,
		DataVolumeID: "vol-0base",
		Volumes:      []VolumeMount{{Name: "tsdb", VolumeID: "vol-0abc-123"}},
	})
	if err != nil {
		t.Fatalf("RenderBootstrap() error = %v", err)
	}
	for _, want := range []string{
		"IS_EDGE=1",
		`TIMEZONE="America/Mexico_City"`,
		"TARGET_SWAP_MB=1024",
		"SELINUX=enforcing",
		"--add-service=http",
		`GRAPHROOT="$MARKER_DIR/containers"`,
		"semanage fcontext -a -e /var/lib/containers/storage",
		`log_driver = "journald"`,
		"mksrv_disk_by_serial 'vol-0base'",
		"mksrv_disk_by_serial 'vol-0abc-123'",
		`$MARKER_DIR/vol/tsdb`,
		".bootstrap-v10",
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

func TestDropEmpty(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{
		"/etc/containers/systemd/a.container": []byte("[Container]\nImage=x\n"),
		"/etc/containers/systemd/b.container": []byte("  \n\t\n"),
		"/etc/containers/systemd/c.container": nil,
	}
	dropEmpty(files)
	if len(files) != 1 {
		t.Fatalf("dropEmpty kept %d files: %v", len(files), files)
	}
	if _, ok := files["/etc/containers/systemd/a.container"]; !ok {
		t.Fatal("dropEmpty removed the non-empty file")
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
