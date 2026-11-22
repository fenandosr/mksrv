// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// complete runs the hidden `__complete` command the generated shell scripts call, and returns
// the candidates it printed (one per line, before the ":<directive>" line).
func complete(t *testing.T, args ...string) (candidates []string, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	app := New(BuildInfo{Version: "dev"}, &out, &errb)
	if err := app.Execute(context.Background(), append([]string{"__complete"}, args...)); err != nil {
		t.Fatalf("__complete %v: %v", args, err)
	}
	// Cobra prints completions on the command's out stream, which mksrv points at stderr.
	for _, line := range strings.Split(errb.String(), "\n") {
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "Completion ended") || line == "" {
			continue
		}
		candidates = append(candidates, line)
	}
	return candidates, out.String()
}

func exampleWorkspace() string {
	return filepath.Clean(filepath.Join("..", "..", "examples", "workspace"))
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestCompletesTenantIDs(t *testing.T) {
	t.Parallel()
	for _, cmd := range [][]string{{"tenant", "apply"}, {"users", "apply"}, {"tenant", "secret-id"}, {"tenant", "mesh"}} {
		got, _ := complete(t, append(append([]string{}, cmd...), "--workspace", exampleWorkspace(), "")...)
		if !has(got, "acme") {
			t.Errorf("%v: want acme among %v", cmd, got)
		}
	}
	// A prefix narrows.
	if got, _ := complete(t, "tenant", "apply", "--workspace", exampleWorkspace(), "zz"); len(got) != 0 {
		t.Errorf("prefix zz must match nothing, got %v", got)
	}
	// secret-id takes ONE id: nothing after it. apply is variadic but does not repeat an id.
	if got, _ := complete(t, "tenant", "secret-id", "acme", "--workspace", exampleWorkspace(), ""); len(got) != 0 {
		t.Errorf("secret-id takes one tenant, got %v", got)
	}
	if got, _ := complete(t, "tenant", "apply", "acme", "--workspace", exampleWorkspace(), ""); has(got, "acme") {
		t.Errorf("an id already typed must not be offered again, got %v", got)
	}
}

func TestCompletesHostNamesAndStacks(t *testing.T) {
	t.Parallel()
	hosts, _ := complete(t, "deploy", "--workspace", exampleWorkspace(), "")
	if len(hosts) == 0 {
		t.Fatal("deploy must complete the example workspace's hosts")
	}
	stacks, _ := complete(t, "deploy", "--stack", "ba")
	if !has(stacks, "base") || !has(stacks, "backup") {
		t.Errorf("--stack ba: want base and backup, got %v", stacks)
	}
	// Comma lists keep what was typed and do not repeat it.
	list, _ := complete(t, "deploy", "--stack", "base,ba")
	if !has(list, "base,backup") || has(list, "base,base") {
		t.Errorf("--stack base,ba: got %v", list)
	}
}

func TestCompletesApprolesOfTheTypedTenant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(exampleWorkspace())); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "tenants", "acme.yaml"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	// The workspace may or may not validate with this block; the completer must work either way.
	if _, err := f.WriteString("storage:\n  approles:\n    - name: worker\n      cidrs: [100.64.0.9/32]\n      kv_read: [database]\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, _ := complete(t, "tenant", "secret-id", "acme", "--workspace", dir, "--approle", "")
	if !has(got, "worker") {
		t.Errorf("want the tenant's approle worker, got %v", got)
	}
	// No tenant typed yet: nothing to complete from.
	if got, _ := complete(t, "tenant", "secret-id", "--workspace", dir, "--approle", ""); len(got) != 0 {
		t.Errorf("without a tenant argument nothing can be offered, got %v", got)
	}
}

func TestCompletionIsQuietWithoutAWorkspace(t *testing.T) {
	t.Parallel()
	empty := t.TempDir()
	for _, args := range [][]string{
		{"tenant", "apply", "--workspace", empty, ""},
		{"deploy", "--workspace", empty, ""},
		{"tenant", "secret-id", "acme", "--workspace", empty, "--approle", ""},
	} {
		got, out := complete(t, args...)
		if len(got) != 0 || out != "" {
			t.Errorf("%v: no workspace must complete nothing and print nothing, got %v / %q", args, got, out)
		}
	}
}

func TestCompletionCommandExistsAndTheExclusiveFlagsAreEnforced(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	app := New(BuildInfo{Version: "dev"}, &out, &errb)
	if err := app.Execute(context.Background(), []string{"completion", "bash"}); err != nil {
		t.Fatalf("completion bash: %v", err)
	}
	if !strings.Contains(errb.String()+out.String(), "bash completion") {
		t.Errorf("completion bash must emit a script")
	}
	err := New(BuildInfo{}, &bytes.Buffer{}, &bytes.Buffer{}).Execute(context.Background(),
		[]string{"tenant", "secret-id", "acme", "--service", "--approle", "x"})
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "none of the others") {
		t.Errorf("--service with --approle must be a usage error, got %v", err)
	}
}
