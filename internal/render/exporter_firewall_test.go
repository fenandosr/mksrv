// SPDX-License-Identifier: Apache-2.0

package render

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/schema"
)

var regexpFiveXX = regexp.MustCompile(`\b5[0-9]{2}\b`)

// postgres_exporter (:9187) and Caddy's metrics (:9019) run with host networking, so they are INPUT
// traffic and firewalld's `public` zone (ssh only) rejected Prometheus with "no route to host":
// neither was ever scraped. These tests RUN the rendered firewall scripts with firewall-cmd and
// systemctl stubbed, because the failure was invisible in the rendered text.

func renderFirewallScript(t *testing.T, stack, host, dst string, monitors []Member) string {
	t.Helper()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx := baseContext()
	ctx.Host.Name = host
	ctx.Host.Stacks = []string{stack}
	ctx.StackMembers = map[string][]Member{"monitor": monitors}
	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog[stack], ctx)
	if err != nil {
		t.Fatal(err)
	}
	script, ok := files[dst]
	if !ok {
		t.Fatalf("stack %s must render %s", stack, dst)
	}
	return string(script)
}

// runFirewallScript runs the script and returns the firewall-cmd calls it made.
func runFirewallScript(t *testing.T, script string, firewalldActive bool) (calls []string, output string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	active := "exit 0"
	if !firewalldActive {
		active = "exit 3"
	}
	stubs := map[string]string{
		"firewall-cmd": "#!/usr/bin/env bash\necho \"$*\" >> " + log + "\n",
		"systemctl":    "#!/usr/bin/env bash\n" + active + "\n",
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "fw.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", path)
	cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	blob, _ := os.ReadFile(log)
	for _, l := range strings.Split(strings.TrimSpace(string(blob)), "\n") {
		if l != "" {
			calls = append(calls, l)
		}
	}
	return calls, string(out)
}

func TestExporterFirewallsOpenTheMetricsPortToTheMonitorHostOnly(t *testing.T) {
	t.Parallel()
	monitors := []Member{{Name: "appd", PrivateIP: "10.20.0.30"}}
	for _, c := range []struct {
		stack, host, dst, port string
	}{
		{"postgres", "core1", "/var/lib/mksrv/stacks/postgres/exporter-firewall.sh", "9187"},
		{"base", "edge", "/var/lib/mksrv/stacks/base/metrics-firewall.sh", "9019"},
	} {
		calls, out := runFirewallScript(t, renderFirewallScript(t, c.stack, c.host, c.dst, monitors), true)
		rule := `rule family="ipv4" source address="10.20.0.30" port port="` + c.port + `" protocol="tcp" accept`
		var runtime, permanent bool
		for _, call := range calls {
			if !strings.Contains(call, rule) {
				t.Errorf("%s: unexpected firewall-cmd call (only the monitor's rule is allowed): %s", c.stack, call)
			}
			if strings.HasPrefix(call, "--permanent") {
				permanent = true
			} else {
				runtime = true
			}
		}
		if !runtime || !permanent {
			t.Errorf("%s: the rule must be added at runtime AND permanently (runtime=%v permanent=%v): %q\n%s", c.stack, runtime, permanent, calls, out)
		}
		// A source rich rule, never a bare port: nothing but the monitor host gains access.
		for _, call := range calls {
			if strings.Contains(call, "--add-port") {
				t.Errorf("%s: must not open a bare port: %s", c.stack, call)
			}
		}
	}
}

func TestExporterFirewallsCoverEveryMonitorHostAndNoneWhenThereIsNone(t *testing.T) {
	t.Parallel()
	two := []Member{{Name: "appd", PrivateIP: "10.20.0.30"}, {Name: "appd2", PrivateIP: "10.20.0.31"}}
	calls, _ := runFirewallScript(t, renderFirewallScript(t, "postgres", "core1", "/var/lib/mksrv/stacks/postgres/exporter-firewall.sh", two), true)
	if len(calls) != 4 { // two hosts, runtime and permanent each
		t.Errorf("two monitor hosts need four calls, got %q", calls)
	}
	calls, out := runFirewallScript(t, renderFirewallScript(t, "postgres", "core1", "/var/lib/mksrv/stacks/postgres/exporter-firewall.sh", nil), true)
	if len(calls) != 0 {
		t.Errorf("no monitor host: nothing to open, got %q", calls)
	}
	if !strings.Contains(out, "rules in place") {
		t.Errorf("the script must still finish cleanly: %s", out)
	}
}

func TestExporterFirewallsDoNothingWithoutFirewalld(t *testing.T) {
	t.Parallel()
	monitors := []Member{{Name: "appd", PrivateIP: "10.20.0.30"}}
	calls, out := runFirewallScript(t, renderFirewallScript(t, "base", "edge", "/var/lib/mksrv/stacks/base/metrics-firewall.sh", monitors), false)
	if len(calls) != 0 || !strings.Contains(out, "firewalld not active") {
		t.Errorf("an inactive firewalld means nothing to open, got %q / %s", calls, out)
	}
}

// The hooks must call the rendered scripts at the path the stacks write them to, and the stacks
// must register them: a hook that points nowhere would fail the deploy of a database host.
func TestFirewallHooksPointAtTheRenderedScripts(t *testing.T) {
	t.Parallel()
	for stack, want := range map[string]string{
		"postgres": "/var/lib/mksrv/stacks/postgres/exporter-firewall.sh",
		"base":     "/var/lib/mksrv/stacks/base/metrics-firewall.sh",
	} {
		catalog, err := engine.Catalog(schema.New())
		if err != nil {
			t.Fatal(err)
		}
		registered := false
		for _, h := range catalog[stack].Hooks.PostDeploy {
			if strings.HasPrefix(h.Run, "hooks/open-") {
				registered = true
				body, err := os.ReadFile(filepath.Join("..", "..", "stacks", stack, filepath.FromSlash(h.Run)))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(body), "bash "+want) {
					t.Errorf("%s hook must run %s:\n%s", stack, want, body)
				}
			}
		}
		if !registered {
			t.Errorf("stack %s must register its open-* post_deploy hook", stack)
		}
	}
}

func TestMonitorProbesAcceptAHealthy404ButNotAnOutage(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx := baseContext()
	ctx.Host.Name = "appd"
	ctx.Host.Stacks = []string{"monitor"}
	ctx.OperatorFQDNs = []string{"vpn.example.com"}
	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog["monitor"], ctx)
	if err != nil {
		t.Fatal(err)
	}
	prom := string(files["/var/lib/mksrv/stacks/monitor/prometheus.yml"])
	if !strings.Contains(prom, "module: [http_reachable]") || strings.Contains(prom, "module: [http_2xx]") {
		t.Errorf("the operator-endpoint probe must use http_reachable:\n%s", prom)
	}
	bb := string(files["/var/lib/mksrv/stacks/monitor/blackbox.yml"])
	if !strings.Contains(bb, "http_reachable:") || !strings.Contains(bb, "404") {
		t.Errorf("blackbox must define http_reachable accepting 404:\n%s", bb)
	}
	// 5xx must still count as down: no 5xx code in the accepted list.
	for _, line := range strings.Split(bb, "\n") {
		if strings.Contains(line, "valid_status_codes") && regexpFiveXX.MatchString(line) {
			t.Errorf("a 5xx must not be a valid status: %s", line)
		}
	}
}
