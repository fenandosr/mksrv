// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestPlanRequiresInfraOnly(t *testing.T) {
	t.Parallel()
	app := New(BuildInfo{Version: "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
	err := app.Execute(context.Background(), []string{"plan"})
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode() = %d, err=%v", ExitCode(err), err)
	}
}

func TestApplyRequiresInfraOnly(t *testing.T) {
	t.Parallel()
	app := New(BuildInfo{Version: "dev"}, &bytes.Buffer{}, &bytes.Buffer{})
	err := app.Execute(context.Background(), []string{"apply"})
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode() = %d, err=%v", ExitCode(err), err)
	}
}

func TestPlanWithoutWorkspaceFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var stderr bytes.Buffer
	app := New(BuildInfo{Version: "dev"}, &bytes.Buffer{}, &stderr)
	err := app.Execute(context.Background(), []string{"plan", "--infra-only", "--workspace", dir})
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode() = %d, err=%v; stderr=%s", ExitCode(err), err, stderr.String())
	}
}

func TestOperatorPublicKeyFromEnvValue(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3Nz test@example"
	t.Setenv("MKSRV_SSH_PUBLIC_KEY", key)
	if got := operatorPublicKey(); got != key {
		t.Fatalf("operatorPublicKey() = %q, want %q", got, key)
	}
}
