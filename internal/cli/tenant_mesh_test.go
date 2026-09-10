// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"
)

func TestTailscaleUpCommand(t *testing.T) {
	t.Parallel()

	base := tailscaleUpCommand("https://vpn.cloud-it.click", "k-abc", "mcps-hpc-01", nil)
	for _, want := range []string{
		"sudo tailscale up",
		"--login-server https://vpn.cloud-it.click",
		"--authkey k-abc",
		"--hostname mcps-hpc-01",
		"--accept-routes=false",
		"--accept-dns=false",
	} {
		if !strings.Contains(base, want) {
			t.Fatalf("command missing %q: %s", want, base)
		}
	}
	if strings.Contains(base, "--advertise-routes") {
		t.Fatalf("no routes declared, should not advertise: %s", base)
	}

	withRoutes := tailscaleUpCommand("https://vpn.cloud-it.click", "k-abc", "mcps-hpc-01",
		[]string{"10.42.42.0/24", "10.52.52.0/24"})
	if !strings.Contains(withRoutes, "--advertise-routes=10.42.42.0/24,10.52.52.0/24") {
		t.Fatalf("advertise-routes wrong: %s", withRoutes)
	}
}
