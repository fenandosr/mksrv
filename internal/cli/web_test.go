// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/model"
)

func TestWebFragment(t *testing.T) {
	t.Parallel()
	w := model.TenantWebEndpoint{Hostname: "files.mcps-epcm.org", Target: "mcps-nextcloud.prod.mksrv:80"}

	if got := webFragmentPath("mcps", w.Hostname); got != "/var/lib/mksrv/caddy.d/25-web-mcps-files-mcps-epcm-org.caddy" {
		t.Fatalf("fragment path = %q", got)
	}
	frag := webFragment(w)
	for _, want := range []string{
		"files.mcps-epcm.org {",
		"reverse_proxy mcps-nextcloud.prod.mksrv:80 {",
		"header_up Host {host}",
		"header_up X-Forwarded-Proto {scheme}",
		"flush_interval -1",
	} {
		if !strings.Contains(frag, want) {
			t.Fatalf("fragment missing %q:\n%s", want, frag)
		}
	}
}

func TestWebOriginPorts(t *testing.T) {
	t.Parallel()
	got := webOriginPorts([]model.TenantWebEndpoint{
		{Target: "a.prod.mksrv:8000"},
		{Target: "b.prod.mksrv:80"},
		{Target: "c.prod.mksrv:80"}, // dup port
		{Target: "bad"},             // no port — skipped
	})
	if strings.Join(got, ",") != "80,8000" {
		t.Fatalf("ports = %v, want [80 8000]", got)
	}
}
