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
