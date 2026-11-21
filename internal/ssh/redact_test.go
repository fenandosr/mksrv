// SPDX-License-Identifier: Apache-2.0

package ssh

import (
	"strings"
	"testing"
)

func TestRedactCommandMasksCredentials(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ in, secret string }{
		"openbao token env, quoted with a newline": {"sudo podman exec -i -e BAO_ADDR=http://127.0.0.1:8200 -e BAO_TOKEN='s.AbC123\n' mksrv-openbao bao 'policy' 'write'", "s.AbC123"},
		"openbao token env, bare":                  {"podman exec -e BAO_TOKEN=s.Plain1 x", "s.Plain1"},
		"kv put password field":                    {"bao 'kv' 'put' 'kv/tenants/x/database' 'password=hunter2' 'dbname=db_x'", "hunter2"},
		"secret id":                                {"bao write auth/approle/login role_id=r 'secret_id=abcd-1234'", "abcd-1234"},
		"connection url":                           {"bao kv put p 'url=postgres://user:S3cret@host:5432/db'", "S3cret"},
		"pg password env":                          {"PGPASSWORD=pw123 psql -c 'select 1'", "pw123"},
	} {
		got := RedactCommand(c.in)
		if strings.Contains(got, c.secret) {
			t.Errorf("%s: secret %q survived: %s", name, c.secret, got)
		}
		if !strings.Contains(got, "<redacted>") {
			t.Errorf("%s: nothing was redacted: %s", name, got)
		}
	}
}

func TestRedactCommandKeepsTheRestReadable(t *testing.T) {
	t.Parallel()
	in := "sudo podman exec -i -e BAO_ADDR=http://127.0.0.1:8200 -e BAO_TOKEN='s.x' mksrv-openbao bao 'policy' 'write' 'mksrv-operator' '-'"
	got := RedactCommand(in)
	for _, keep := range []string{"BAO_ADDR=http://127.0.0.1:8200", "mksrv-openbao", "'policy' 'write' 'mksrv-operator'"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("lost %q: %s", keep, got)
		}
	}
	// A command with no secret is returned unchanged.
	plain := "sudo systemctl restart mksrv-mailserver.service"
	if RedactCommand(plain) != plain {
		t.Fatalf("a plain command must not change: %s", RedactCommand(plain))
	}
}
