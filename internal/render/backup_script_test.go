// SPDX-License-Identifier: Apache-2.0

package render

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/schema"
)

// These tests RUN the rendered backup script with `podman` and `curl` replaced by
// stubs, because the bugs they guard against were invisible to a test that only
// looks at the script's text: for at least nine days every run skipped the
// OpenBao snapshot and every Keycloak export, and still ended with "done".

const stubPodman = `#!/usr/bin/env bash
# restic and pg_dump run through podman in the script.
args="$*"
case "$args" in
  *pg_dump*) [ -n "$STUB_FAIL_PG" ] && { echo "pg_dump: error: connection failed" >&2; exit 1; }; echo "DUMP"; exit 0 ;;
esac
exit 0
`

// stubCurl answers like the real services: Keycloak's partial-export is 404 for a GET and
// only works as a POST; the raft snapshot is written to the -o file.
const stubCurl = `#!/usr/bin/env bash
method=GET; out=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -X) method="$2"; shift 2 ;;
    -o) out="$2"; shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
case "$url" in
  */openid-connect/token) echo '{"access_token":"tok123"}' ;;
  *partial-export*)
    if [ "$method" != POST ]; then echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; fi
    echo '{"realm":"x"}' ;;
  */v1/sys/storage/raft/snapshot)
    if [ -n "$STUB_FAIL_RAFT" ]; then echo "curl: (22) The requested URL returned error: 403" >&2; exit 22; fi
    echo "SNAPSHOT" > "$out" ;;
  *) exit 0 ;;
esac
`

// runBackupScript renders the script, points its fixed paths at a temp dir and runs it.
func runBackupScript(t *testing.T, env []string, previousProm string) (output string, exitCode int, prom string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx := baseContext()
	ctx.Host.Name = "appd"
	ctx.Host.Stacks = []string{"backup"}
	files, err := Stack(filepath.Clean(filepath.Join("..", "..", "stacks")), catalog["backup"], ctx)
	if err != nil {
		t.Fatal(err)
	}
	script := string(files["/var/lib/mksrv/stacks/backup/backup.sh"])

	root := t.TempDir()
	script = strings.ReplaceAll(script, "/var/lib/mksrv", root+"/mksrv")
	if err := os.MkdirAll(root+"/mksrv/stacks/backup", 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := "RESTIC_REPOSITORY=s3:example\nPG_PRIMARY_IP=10.0.0.1\nPG_SUPERPASS=x\nBAO_ADDR=http://10.0.0.2:8200\nBAO_TOKEN=t\n" +
		"KC_URL=https://auth.example.com\nKC_ADMIN_PASSWORD=p\nREALMS='a b'\nDBS='db_a db_b'\n"
	if err := os.WriteFile(root+"/mksrv/stacks/backup/backup.env", []byte(envFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if previousProm != "" {
		if err := os.MkdirAll(root+"/mksrv/metrics", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(root+"/mksrv/metrics/backup.prom", []byte(previousProm), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	for name, body := range map[string]string{"podman": stubPodman, "curl": stubCurl} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scriptPath := filepath.Join(root, "backup.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + root}, env...)
	out, runErr := cmd.CombinedOutput()
	code := 0
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if runErr != nil {
		t.Fatalf("running the script: %v\n%s", runErr, out)
	}
	blob, _ := os.ReadFile(root + "/mksrv/metrics/backup.prom")
	return string(out), code, string(blob)
}

func TestBackupScriptCompletesAndMarksSuccessWhenEveryComponentIsCollected(t *testing.T) {
	t.Parallel()
	out, code, prom := runBackupScript(t, nil, "")
	if code != 0 {
		t.Fatalf("a clean run must exit 0, got %d:\n%s", code, out)
	}
	for _, want := range []string{"keycloak export master", "keycloak export a", "keycloak export b", "openbao raft snapshot", "pg_dump db_b", "[mksrv-backup] done"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "failed") {
		t.Fatalf("nothing should have failed (the stub Keycloak only answers a POST):\n%s", out)
	}
	for _, want := range []string{"mksrv_backup_failures 0", "mksrv_backup_last_success_seconds ", `component="openbao"} 1`, `component="keycloak"} 1`, `component="postgres"} 1`} {
		if !strings.Contains(prom, want) {
			t.Errorf("metrics missing %q:\n%s", want, prom)
		}
	}
}

func TestBackupScriptFailsLoudlyAndKeepsTheLastSuccessWhenAComponentFails(t *testing.T) {
	t.Parallel()
	previous := "mksrv_backup_last_success_seconds 111\n"
	out, code, prom := runBackupScript(t, []string{"STUB_FAIL_RAFT=1"}, previous)
	if code != 1 {
		t.Fatalf("a run that lost a component must exit 1 (so the unit fails and `backup run` reports it), got %d:\n%s", code, out)
	}
	for _, want := range []string{"raft snapshot failed", "returned error: 403", "WITH 1 FAILURE(S)", "INCOMPLETE"} {
		if !strings.Contains(out, want) {
			t.Errorf("the journal must explain the failure; missing %q:\n%s", want, out)
		}
	}
	// The failure is in the metrics, and the success timestamp did NOT move (BackupStale needs the series).
	for _, want := range []string{"mksrv_backup_failures 1", `component="openbao"} 0`, `component="postgres"} 1`, `component="keycloak"} 1`, "mksrv_backup_last_success_seconds 111\n"} {
		if !strings.Contains(prom, want) {
			t.Errorf("metrics missing %q:\n%s", want, prom)
		}
	}
	// The restic snapshot is still taken: an incomplete snapshot beats none.
	if strings.Contains(out, "restic backup failed") {
		t.Fatalf("the snapshot must still be written:\n%s", out)
	}
}

func TestBackupScriptFlagsAFailedPostgresDump(t *testing.T) {
	t.Parallel()
	out, code, prom := runBackupScript(t, []string{"STUB_FAIL_PG=1"}, "")
	if code != 1 || !strings.Contains(out, "pg_dump db_a failed") {
		t.Fatalf("a failed dump must fail the run, got %d:\n%s", code, out)
	}
	if !strings.Contains(prom, `component="postgres"} 0`) || strings.Contains(prom, "mksrv_backup_last_success_seconds") {
		t.Fatalf("postgres must be marked failed and no success recorded when there never was one:\n%s", prom)
	}
}
