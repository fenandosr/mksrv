// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/schema"
)

// These tests RUN the rendered snapshot freshness check (ADR 0046) against a fake IMDS and a fake
// EC2 endpoint. The check is what tells the operator that a DLM policy silently stopped selecting
// volumes, so a test that only read its text would prove nothing.

type fakeVolume struct {
	id, name, tier, rpo string
}

type fakeSnapshot struct {
	volume, status string
	age            time.Duration
}

func renderedSnapshotCheck(t *testing.T) string {
	t.Helper()
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
	script, ok := files["/var/lib/mksrv/stacks/backup/snapshot-check.py"]
	if !ok {
		t.Fatal("the backup stack must render snapshot-check.py")
	}
	return string(script)
}

func tagSet(kv ...string) string {
	var b strings.Builder
	b.WriteString("<tagSet>")
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, "<item><key>%s</key><value>%s</value></item>", kv[i], kv[i+1])
	}
	b.WriteString("</tagSet>")
	return b.String()
}

// fakeAWS serves IMDSv2 and the two EC2 calls. failSnapshots makes DescribeSnapshots a 500;
// truncated adds a nextToken.
type fakeAWS struct {
	volumes       []fakeVolume
	snapshots     []fakeSnapshot
	failSnapshots bool
	truncated     bool

	mu       sync.Mutex
	ec2Auth  []string
	ec2Token []string
	ec2Query []string
}

func (f *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/latest/api/token" && r.Method == http.MethodPut:
		fmt.Fprint(w, "imds-token")
	case r.URL.Path == "/latest/meta-data/iam/security-credentials/":
		fmt.Fprint(w, "mksrv-role\n")
	case r.URL.Path == "/latest/meta-data/iam/security-credentials/mksrv-role":
		fmt.Fprint(w, `{"AccessKeyId":"AKIDTEST","SecretAccessKey":"secret-test","Token":"TOKENTEST"}`)
	case r.URL.Path == "/latest/meta-data/placement/region":
		fmt.Fprint(w, "us-east-1")
	case r.URL.Path == "/":
		f.mu.Lock()
		f.ec2Auth = append(f.ec2Auth, r.Header.Get("Authorization"))
		f.ec2Token = append(f.ec2Token, r.Header.Get("X-Amz-Security-Token"))
		f.ec2Query = append(f.ec2Query, r.URL.RawQuery)
		f.mu.Unlock()
		next := ""
		if f.truncated {
			next = "<nextToken>more</nextToken>"
		}
		switch r.URL.Query().Get("Action") {
		case "DescribeVolumes":
			var b strings.Builder
			b.WriteString(`<DescribeVolumesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><volumeSet>`)
			for _, v := range f.volumes {
				fmt.Fprintf(&b, "<item><volumeId>%s</volumeId>%s</item>", v.id,
					tagSet("Name", v.name, "mksrv:host", "core1", "mksrv:env", "prod", "mksrv:snapshot", v.tier, "mksrv:snapshot-rpo-hours", v.rpo))
			}
			fmt.Fprintf(&b, "</volumeSet>%s</DescribeVolumesResponse>", next)
			fmt.Fprint(w, b.String())
		case "DescribeSnapshots":
			if f.failSnapshots {
				http.Error(w, "<Error>boom</Error>", http.StatusInternalServerError)
				return
			}
			var b strings.Builder
			b.WriteString(`<DescribeSnapshotsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><snapshotSet>`)
			for i, s := range f.snapshots {
				fmt.Fprintf(&b, "<item><snapshotId>snap-%d</snapshotId><volumeId>%s</volumeId><status>%s</status><startTime>%s</startTime></item>",
					i, s.volume, s.status, time.Now().Add(-s.age).UTC().Format("2006-01-02T15:04:05.000Z"))
			}
			fmt.Fprintf(&b, "</snapshotSet>%s</DescribeSnapshotsResponse>", next)
			fmt.Fprint(w, b.String())
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	default:
		http.NotFound(w, r)
	}
}

func runSnapshotCheck(t *testing.T, f *fakeAWS) (stderr string, code int, prom string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	script := filepath.Join(dir, "snapshot-check.py")
	if err := os.WriteFile(script, []byte(renderedSnapshotCheck(t)), 0o755); err != nil {
		t.Fatal(err)
	}
	metrics := filepath.Join(dir, "metrics")
	cmd := exec.Command("python3", script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "MKSRV_METRICS_DIR=" + metrics, "MKSRV_IMDS_URL=" + srv.URL, "MKSRV_EC2_URL=" + srv.URL}
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the check: %v\n%s", err, out)
	}
	blob, _ := os.ReadFile(filepath.Join(metrics, "snapshots.prom"))
	return string(out), code, string(blob)
}

func TestSnapshotCheckReportsCoveragePerTier(t *testing.T) {
	t.Parallel()
	f := &fakeAWS{
		volumes: []fakeVolume{
			{"vol-a", "mksrv-prod-edge-maildata", "critical", "24"}, // fresh
			{"vol-b", "mksrv-prod-core1-pgdata", "critical", "24"},  // newest is 40 h old: stale
			{"vol-c", "mksrv-prod-core2-pgdata", "critical", "24"},  // never snapshotted
			{"vol-d", "mksrv-prod-core1-baoraft", "critical", "6"},  // a 6 h RPO: 12 h old is stale
			{"vol-e", "mksrv-prod-appd-data", "standard", "24"},     // fresh
			{"vol-f", "mksrv-prod-appd-tsdb", "low", "24"},          // only a snapshot that is still pending
		},
		snapshots: []fakeSnapshot{
			{"vol-a", "completed", 1 * time.Hour},
			{"vol-a", "completed", 25 * time.Hour}, // older ones do not matter
			{"vol-b", "completed", 40 * time.Hour},
			{"vol-d", "completed", 12 * time.Hour},
			{"vol-e", "completed", 2 * time.Hour},
			{"vol-f", "pending", 1 * time.Hour},
			{"vol-f", "error", 3 * time.Hour},
		},
	}
	stderr, code, prom := runSnapshotCheck(t, f)
	if code != 0 {
		t.Fatalf("a check that could ask EC2 exits 0 even when volumes are stale (the alert is Prometheus's), got %d:\n%s", code, stderr)
	}
	for _, want := range []string{
		"mksrv_snapshot_check_success 1",
		`mksrv_snapshot_volumes{tier="critical"} 4`,
		`mksrv_snapshot_coverage_ratio{tier="critical"} 0.2500`, // only vol-a of four is within its RPO
		`mksrv_snapshot_coverage_ratio{tier="standard"} 1.0000`,
		`mksrv_snapshot_coverage_ratio{tier="low"} 0.0000`, // a pending or failed snapshot protects nothing
		`volume="mksrv-prod-core2-pgdata",host="core1",volume_id="vol-c"} 0`,
		"mksrv_snapshot_grace_seconds 14400",
	} {
		if !strings.Contains(prom, want) {
			t.Errorf("metrics missing %q:\n%s", want, prom)
		}
	}
	for _, line := range strings.Split(prom, "\n") {
		if strings.Contains(line, `volume_id="vol-a"`) && strings.HasSuffix(line, " 0") {
			t.Errorf("vol-a has a completed snapshot: %s", line)
		}
	}
	if !strings.Contains(stderr, "stale: critical mksrv-prod-core2-pgdata") {
		t.Errorf("the journal must name the stale volumes:\n%s", stderr)
	}
}

func TestSnapshotCheckSignsItsRequestsWithTheInstanceRole(t *testing.T) {
	t.Parallel()
	f := &fakeAWS{volumes: []fakeVolume{{"vol-a", "x", "critical", "24"}}}
	runSnapshotCheck(t, f)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ec2Auth) != 2 {
		t.Fatalf("expected one DescribeVolumes and one DescribeSnapshots, got %d calls", len(f.ec2Auth))
	}
	for i, auth := range f.ec2Auth {
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDTEST/") || !strings.Contains(auth, "/us-east-1/ec2/aws4_request") ||
			!strings.Contains(auth, "SignedHeaders=host;x-amz-date;x-amz-security-token") {
			t.Errorf("call %d: unexpected Authorization %q", i, auth)
		}
		if f.ec2Token[i] != "TOKENTEST" {
			t.Errorf("call %d must carry the role's session token, got %q", i, f.ec2Token[i])
		}
	}
	// The queries select this environment's tagged volumes only.
	for _, q := range f.ec2Query {
		if !strings.Contains(q, "Filter.1.Value.1=prod") || !strings.Contains(q, "Filter.2.Value.1=mksrv%3Asnapshot") {
			t.Errorf("query must filter on mksrv:env and the snapshot tag: %s", q)
		}
	}
}

func TestSnapshotCheckFailsLoudlyWhenEC2CannotBeAsked(t *testing.T) {
	t.Parallel()
	for name, f := range map[string]*fakeAWS{
		"DescribeSnapshots fails":     {volumes: []fakeVolume{{"vol-a", "x", "critical", "24"}}, failSnapshots: true},
		"a second page would be lost": {volumes: []fakeVolume{{"vol-a", "x", "critical", "24"}}, truncated: true},
	} {
		stderr, code, prom := runSnapshotCheck(t, f)
		if code != 1 || !strings.Contains(stderr, "snapshot check failed") {
			t.Errorf("%s: want exit 1 and a message, got %d:\n%s", name, code, stderr)
		}
		if !strings.Contains(prom, "mksrv_snapshot_check_success 0") || strings.Contains(prom, "mksrv_snapshot_coverage_ratio") {
			t.Errorf("%s: must report failure and not invent coverage:\n%s", name, prom)
		}
	}
}

// The signature is checked against the test vector AWS publishes for Signature Version 4
// (GET, iam ListUsers, 2015-08-30T12:36:00Z): an implementation that gets any step wrong cannot
// reproduce it.
func TestSnapshotCheckSigV4MatchesAWSTestVector(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "snapshot_check.py"), []byte(renderedSnapshotCheck(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	prog := `
import datetime, snapshot_check as m
now = datetime.datetime(2015, 8, 30, 12, 36, 0, tzinfo=datetime.timezone.utc)
auth, _, _ = m.sigv4_get("iam.amazonaws.com", "us-east-1", "iam", [("Action", "ListUsers"), ("Version", "2010-05-08")],
    {"content-type": "application/x-www-form-urlencoded; charset=utf-8"}, "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", now)
print(auth)
`
	cmd := exec.Command("python3", "-c", prog)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7") {
		t.Fatalf("signature does not match AWS's published test vector:\n%s", out)
	}
}
