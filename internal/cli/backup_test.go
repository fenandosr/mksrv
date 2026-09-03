// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/workspace"
)

func TestResticRepo(t *testing.T) {
	t.Parallel()
	f := &fleet{data: workspace.Data{Deployment: model.Deployment{
		Env: "prod", AWS: model.AWSConfig{Region: "us-east-1"},
	}}}
	if got := f.resticRepo(); got != "s3:s3.us-east-1.amazonaws.com/mksrv-prod-backups" {
		t.Fatalf("resticRepo() = %q", got)
	}
}

func TestBackupPolicyHCL(t *testing.T) {
	t.Parallel()
	if !strings.Contains(backupPolicyHCL, `path "sys/storage/raft/snapshot"`) ||
		!strings.Contains(backupPolicyHCL, `capabilities = ["read"]`) {
		t.Fatalf("backup policy wrong:\n%s", backupPolicyHCL)
	}
	if strings.Contains(backupPolicyHCL, "kv/") || strings.Contains(backupPolicyHCL, "write") {
		t.Fatalf("backup policy too broad:\n%s", backupPolicyHCL)
	}
}

func TestShellQuote(t *testing.T) {
	t.Parallel()
	if got := shellQuote("a b"); got != "'a b'" {
		t.Fatalf("shellQuote = %q", got)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("shellQuote(quote) = %q", got)
	}
}

func TestNewBackupCommand(t *testing.T) {
	t.Parallel()
	cmd := (&App{}).newBackupCommand(&globalOptions{})
	var subs []string
	for _, c := range cmd.Commands() {
		subs = append(subs, c.Name())
	}
	for _, want := range []string{"run", "list"} {
		if !slices.Contains(subs, want) {
			t.Fatalf("backup subcommand %q missing from %v", want, subs)
		}
	}
}
