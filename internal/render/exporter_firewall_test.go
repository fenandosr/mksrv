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

// A site address that is a bare IP makes Caddy serve HTTPS on that port; Prometheus scrapes with
// plain HTTP, so the metrics vhost must say http:// (the earlier assertion matched the substring
// without it and could not see the difference).
func TestCaddyMetricsVhostIsPlainHTTP(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx := baseContext()
	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog["base"], ctx)
	if err != nil {
		t.Fatal(err)
	}
	caddyfile := string(files["/var/lib/mksrv/stacks/base/Caddyfile"])
	if !strings.Contains(caddyfile, "\nhttp://10.20.0.10:9019 {") {
		t.Errorf("the metrics vhost must be http://<private ip>:9019:\n%s", caddyfile)
	}
	if strings.Contains(caddyfile, "\n10.20.0.10:9019 {") {
		t.Error("a bare-IP site address would serve HTTPS")
	}
}

// The reload hook must reload gracefully, skip a Caddy that is not running, and fail loudly when the
// reload itself fails.
func TestReloadCaddyHook(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "stacks", "base", "hooks", "reload-caddy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(running, reloadFails bool) (string, int) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		exists, state := "exit 0", "echo true"
		if !running {
			exists, state = "exit 1", "echo false"
		}
		reload := "exit 0"
		if reloadFails {
			reload = "echo 'adapting config: bad directive' >&2; exit 1"
		}
		podman := "#!/usr/bin/env bash\ncase \"$1\" in\n  container) " + exists + " ;;\n  inspect) " + state + " ;;\n  exec) " + reload + " ;;\nesac\n"
		if err := os.WriteFile(filepath.Join(bin, "podman"), []byte(podman), 0o755); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(dir, "hook.sh")
		if err := os.WriteFile(script, body, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", script)
		cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return string(out), code
	}
	if out, code := run(true, false); code != 0 || !strings.Contains(out, "caddy reloaded") {
		t.Errorf("a running Caddy is reloaded, got %d: %s", code, out)
	}
	if out, code := run(false, false); code != 0 || !strings.Contains(out, "not running") {
		t.Errorf("a Caddy that is not running is skipped, got %d: %s", code, out)
	}
	if out, code := run(true, true); code == 0 || !strings.Contains(out, "bad directive") {
		t.Errorf("a failing reload must fail the deploy, got %d: %s", code, out)
	}
}

// blackbox_exporter reads its modules at start, so the monitor hook must reload it as well as
// Prometheus: a new module was unknown to the running exporter and every probe using it got 400.
func TestMonitorHookReloadsBlackboxAndRestartsOnlyWhenReloadFails(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "stacks", "monitor", "hooks", "reload-prometheus.sh"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(blackboxReloadWorks, blackboxActive bool) (out string, restarted bool) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(dir, "restarts.log")
		active := "exit 0"
		if !blackboxActive {
			active = "exit 3"
		}
		// podman exec <container> wget ... : the Prometheus reload works, the blackbox one as requested.
		bbReload := "exit 0"
		if !blackboxReloadWorks {
			bbReload = "exit 1"
		}
		stubs := map[string]string{
			"systemctl": "#!/usr/bin/env bash\ncase \"$1\" in\n  is-active) " + active + " ;;\n  restart) echo \"$2\" >> " + log + " ;;\nesac\n",
			"podman":    "#!/usr/bin/env bash\ncase \"$*\" in\n  *mksrv-blackbox*) " + bbReload + " ;;\n  *) exit 0 ;;\nesac\n",
		}
		for name, b := range stubs {
			if err := os.WriteFile(filepath.Join(bin, name), []byte(b), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		script := filepath.Join(dir, "hook.sh")
		if err := os.WriteFile(script, body, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", script)
		cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
		o, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hook failed: %v\n%s", err, o)
		}
		blob, _ := os.ReadFile(log)
		return string(o), strings.Contains(string(blob), "mksrv-blackbox.service")
	}
	if out, restarted := run(true, true); !strings.Contains(out, "blackbox reloaded") || restarted {
		t.Errorf("a working reload must not restart blackbox: %q restarted=%v", out, restarted)
	}
	if out, restarted := run(false, true); !strings.Contains(out, "blackbox restarted") || !restarted {
		t.Errorf("a failing reload falls back to a restart: %q restarted=%v", out, restarted)
	}
	if out, restarted := run(true, false); !strings.Contains(out, "blackbox not running") || restarted {
		t.Errorf("a host without blackbox is skipped: %q restarted=%v", out, restarted)
	}
}

// Caddy's admin API answers 403 to a request whose Host is not its own address. The metrics vhost
// proxies to it, and reverse_proxy forwards the client's Host (<private ip>:9019) unless told
// otherwise. Reproduced with the real Caddy 2.8 image: without the directive 403, with it 200.
func TestCaddyMetricsVhostRewritesHostForTheAdminAPI(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatal(err)
	}
	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog["base"], baseContext())
	if err != nil {
		t.Fatal(err)
	}
	caddyfile := string(files["/var/lib/mksrv/stacks/base/Caddyfile"])
	i := strings.Index(caddyfile, "http://10.20.0.10:9019 {")
	if i < 0 {
		t.Fatalf("metrics vhost missing:\n%s", caddyfile)
	}
	block := caddyfile[i:]
	if j := strings.Index(block, "\n}\n"); j > 0 {
		block = block[:j]
	}
	if !strings.Contains(block, "reverse_proxy 127.0.0.1:2019 {") || !strings.Contains(block, "header_up Host {upstream_hostport}") {
		t.Errorf("the metrics proxy must rewrite Host to the admin address:\n%s", block)
	}
}
