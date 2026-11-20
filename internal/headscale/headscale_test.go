// SPDX-License-Identifier: Apache-2.0

package headscale

import (
	"context"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/ssh"
)

type fakeRunner struct {
	responses map[string]string
	calls     []string
}

func (f *fakeRunner) Run(_ context.Context, command string) (ssh.Result, error) {
	f.calls = append(f.calls, command)
	for key, out := range f.responses {
		if strings.Contains(command, key) {
			return ssh.Result{Stdout: out}, nil
		}
	}
	return ssh.Result{Stdout: "[]"}, nil
}

func TestEnsureUserParsesNumericID(t *testing.T) {
	t.Parallel()
	fake := &fakeRunner{responses: map[string]string{
		"'users' 'list'": `[{"id":7,"name":"bitabit","created_at":{"seconds":1}}]`,
	}}
	id, err := New(fake).EnsureUser(context.Background(), "bitabit")
	if err != nil {
		t.Fatalf("EnsureUser() error = %v", err)
	}
	if id != "7" {
		t.Fatalf("id = %q, want 7", id)
	}
	for _, c := range fake.calls {
		if strings.Contains(c, "'users' 'create'") {
			t.Fatalf("created an existing user: %s", c)
		}
	}
}

func TestPolicyIsolatesTenantsAndAddsRoutes(t *testing.T) {
	t.Parallel()
	got := Policy([]PolicyTenant{
		{ID: "bitabit"},
		{ID: "mcps", Routes: []string{"10.1.0.0/24"}, WebPorts: []string{"80", "8000"}},
	})
	for _, want := range []string{
		`"src": ["mksrv-fleet@"], "dst": ["mksrv-fleet@:*"]`,
		`"src": ["bitabit@"], "dst": ["bitabit@:*"]`,
		`"src": ["mcps@"], "dst": ["mksrv-fleet@:22,80,443,3000,3010-3099,4450-4459,5050,5432,5433,6379,8030-8039,8090,8200,9090"]`,
		`"src": ["mcps@"], "dst": ["10.1.0.0/24:*"]`,
		`"src": ["mksrv-fleet@"], "dst": ["mcps@:80,8000"]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("policy missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"dst": ["bitabit@:*"], "src": ["mcps@"]`) || strings.Contains(got, `"src": ["mcps@"], "dst": ["bitabit@`) {
		t.Fatalf("policy allows cross-tenant access:\n%s", got)
	}
}

func TestPreAuthKeyExtractsKey(t *testing.T) {
	t.Parallel()
	fake := &fakeRunner{responses: map[string]string{
		"'preauthkeys' 'create'": `{"id":"1","key":"abc123def456","user":{"id":"1"},"reusable":false}`,
	}}
	key, err := New(fake).PreAuthKey(context.Background(), "1", 3600000000000, false)
	if err != nil {
		t.Fatalf("PreAuthKey() error = %v", err)
	}
	if key != "abc123def456" {
		t.Fatalf("key = %q", key)
	}
}

func TestExpireAPIKeysExceptKeepsTheNewOne(t *testing.T) {
	t.Parallel()
	fake := &fakeRunner{responses: map[string]string{
		"'apikeys' 'list'": `[{"prefix":"old1"},{"prefix":"keepme"},{"prefix":"old2"}]`,
	}}
	if err := New(fake).ExpireAPIKeysExcept(context.Background(), "keepme"); err != nil {
		t.Fatalf("ExpireAPIKeysExcept() error = %v", err)
	}
	var expired []string
	for _, c := range fake.calls {
		if strings.Contains(c, "'apikeys' 'expire'") {
			expired = append(expired, c)
		}
	}
	if len(expired) != 2 {
		t.Fatalf("want 2 expire calls (old1, old2), got %d: %v", len(expired), expired)
	}
	for _, c := range expired {
		if strings.Contains(c, "keepme") {
			t.Fatalf("expired the key we said to keep: %s", c)
		}
	}
}

// A `web:` target may be a node of another tenant (staging publishing an app that
// runs on the production tenant's host): the fleet gets a rule for that exact
// ip:port, de-duplicated across tenants, and tenants still cannot reach each other.
func TestPolicyOpensFleetToWebOriginsByIP(t *testing.T) {
	t.Parallel()
	got := Policy([]PolicyTenant{
		{ID: "hg", WebPorts: []string{"8000"}, WebOrigins: []string{"100.64.0.11:8000"}},
		{ID: "hgstg", WebPorts: []string{"8101"}, WebOrigins: []string{"100.64.0.11:8101", "100.64.0.11:8000"}},
	})
	if !strings.Contains(got, `{ "action": "accept", "src": ["mksrv-fleet@"], "dst": ["100.64.0.11:8101"] }`) {
		t.Fatalf("missing fleet -> origin ip rule:\n%s", got)
	}
	if n := strings.Count(got, `"dst": ["100.64.0.11:8000"]`); n != 1 {
		t.Fatalf("origin rule not de-duplicated (%d):\n%s", n, got)
	}
	if strings.Contains(got, `"src": ["hgstg@"], "dst": ["hg@`) || strings.Contains(got, `"src": ["hgstg@"], "dst": ["100.64.0.11`) {
		t.Fatalf("a tenant must not gain access to another tenant's node:\n%s", got)
	}
}
