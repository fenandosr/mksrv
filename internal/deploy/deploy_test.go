// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/render"
	"github.com/fenandosr/mksrv/internal/schema"
	"github.com/fenandosr/mksrv/internal/ssh"
)

func TestRenderBootstrapEdge(t *testing.T) {
	t.Parallel()
	script, err := RenderBootstrap(BootstrapParams{
		IsEdge:       true,
		NAT:          true,
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
		"--add-masquerade",
		"net.ipv4.ip_forward = 1",
		`GRAPHROOT="$MARKER_DIR/containers"`,
		"semanage fcontext -a -e /var/lib/containers/storage",
		`log_driver = "journald"`,
		"mksrv_disk_by_serial 'vol-0base'",
		"mksrv_disk_by_serial 'vol-0abc-123'",
		`$MARKER_DIR/vol/tsdb`,
		".bootstrap-v11",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bootstrap script missing %q", want)
		}
	}
}

func TestRenderBootstrapNonNATHostHasNoMasquerade(t *testing.T) {
	t.Parallel()
	script, err := RenderBootstrap(BootstrapParams{IsEdge: false})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "--add-masquerade") || strings.Contains(script, "ip_forward") {
		t.Fatal("a non-NAT host must not configure masquerade")
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

func TestTailscaleUnit(t *testing.T) {
	t.Parallel()
	unit, err := renderText("tailscale.container", tailscaleUnit, MeshParams{
		LoginServer: "https://vpn.mksrv.example",
		Hostname:    "prod-edge",
		Image:       MeshImage,
	})
	if err != nil {
		t.Fatalf("renderText() error = %v", err)
	}
	body := string(unit)
	for _, want := range []string{
		// container_t denies the tun_socket class needed to create a real TUN
		// device, silently (dontaudit) -- confirmed live on the prod edge:
		// tstun.New("tailscale0"): permission denied, no AVC in ausearch even
		// with dontaudit disabled. Without this the mesh node can never come up.
		"SecurityLabelDisable=true",
		"DeviceAllow=/dev/net/tun rwm",
		// Unquoted, systemd's Environment= splits an embedded space into a
		// second bogus assignment, so --accept-routes never reached tailscaled.
		`Environment="TS_EXTRA_ARGS=--login-server=https://vpn.mksrv.example --accept-routes"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("tailscale unit missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Environment=TS_EXTRA_ARGS=--login-server") {
		t.Fatalf("TS_EXTRA_ARGS must be quoted as a single value:\n%s", body)
	}
}

func TestBindMountedFilesOnlyListsRenderedSingleFileSources(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{
		"/etc/containers/systemd/mksrv-prom.container": []byte("[Container]\n" +
			"Volume=/var/lib/mksrv/stacks/monitor/rules.yml:/etc/prometheus/rules.yml:Z,ro\n" +
			"Volume=/var/lib/mksrv/stacks/monitor/not-rendered.yml:/etc/x.yml:Z,ro\n" +
			"Volume=mksrv-tsdb:/prometheus\n" +
			"Volume=/var/lib/mksrv/vol/tsdb:/data\n"),
		"/var/lib/mksrv/stacks/monitor/rules.yml": []byte("groups: []\n"),
		"/var/lib/mksrv/stacks/monitor/other.yml": []byte("x: 1\n"), // rendered but mounted by nothing
	}
	got := bindMountedFiles(files)
	if len(got) != 1 || !got["/var/lib/mksrv/stacks/monitor/rules.yml"] {
		t.Fatalf("want only the rendered, mounted file, got %v", got)
	}
}

// A hard link stands in for a container's bind mount: it keeps pointing at the inode it was
// created from. The script must change that inode, not replace it.
func TestKeepInodeScriptRewritesInPlace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "rules.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mount := filepath.Join(dir, "mount-view")
	if err := os.Link(path, mount); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)

	run := func(p, content string) {
		cmd := exec.Command("sh", "-c", keepInodeScript(p, 0o644))
		cmd.Stdin = strings.NewReader(content)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script: %v\n%s", err, out)
		}
	}
	run(path, "new content\n")

	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("the file must keep its inode")
	}
	if got, _ := os.ReadFile(mount); string(got) != "new content\n" {
		t.Fatalf("the bind-mount view must see the new content, got %q", got)
	}
	if after.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", after.Mode().Perm())
	}

	// A file that does not exist yet is created, with no temp file left behind.
	fresh := filepath.Join(dir, "fresh", "new.yml")
	run(fresh, "created\n")
	if got, _ := os.ReadFile(fresh); string(got) != "created\n" {
		t.Fatalf("new file content = %q", got)
	}
	if _, err := os.Stat(fresh + ".mksrv.tmp"); err == nil {
		t.Error("the temp file must be gone")
	}
}

type recordingRunner struct {
	writeFileCalls int
	runInputCmd    string
	runInputBody   string
}

func (r *recordingRunner) Run(context.Context, string) (ssh.Result, error) { return ssh.Result{}, nil }
func (r *recordingRunner) RunInput(_ context.Context, cmd string, in []byte) (ssh.Result, error) {
	r.runInputCmd, r.runInputBody = cmd, string(in)
	return ssh.Result{}, nil
}
func (r *recordingRunner) WriteFileSudo(context.Context, string, []byte, fs.FileMode) error {
	r.writeFileCalls++
	return nil
}

func TestWriteStackFileChoosesTheWriteMethod(t *testing.T) {
	t.Parallel()
	plain := &recordingRunner{}
	if err := writeStackFile(context.Background(), plain, "/var/lib/mksrv/x.conf", []byte("a"), false); err != nil {
		t.Fatal(err)
	}
	if plain.writeFileCalls != 1 || plain.runInputCmd != "" {
		t.Errorf("an ordinary file keeps the atomic write, got %+v", plain)
	}
	bound := &recordingRunner{}
	if err := writeStackFile(context.Background(), bound, "/var/lib/mksrv/x.conf", []byte("payload"), true); err != nil {
		t.Fatal(err)
	}
	if bound.writeFileCalls != 0 || !strings.HasPrefix(bound.runInputCmd, "sudo -n sh -c ") || bound.runInputBody != "payload" {
		t.Errorf("a bind-mounted file must be rewritten in place via RunInput, got %+v", bound)
	}
}

// The detection must work on the real templates, not only on a hand-written example: the paths a
// Quadlet mounts have to be the paths the same stack renders.
func TestBindMountedFilesFindsPrometheusConfigInTheRealMonitorStack(t *testing.T) {
	t.Parallel()
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatal(err)
	}
	files, err := render.Stack(filepath.Join("..", "..", "stacks"), catalog["monitor"], render.Context{
		Env:       "prod",
		Timezone:  "Etc/UTC",
		Host:      render.HostView{Name: "appd", PrivateIP: "10.20.0.30", Stacks: []string{"monitor"}},
		Fleet:     []render.Member{{Name: "appd", PrivateIP: "10.20.0.30", Role: "data"}},
		Endpoints: render.Endpoints{RootDomain: "example.com"},
		Images:    map[string]string{},
		Retention: (&model.RetentionConfig{}).Resolved(),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := bindMountedFiles(files)
	for _, want := range []string{
		"/var/lib/mksrv/stacks/monitor/prometheus.yml",
		"/var/lib/mksrv/stacks/monitor/prometheus-rules.yml",
	} {
		if !got[want] {
			t.Errorf("%s is bind-mounted by the monitor Quadlet and must be written in place; detected %v", want, got)
		}
	}
}
