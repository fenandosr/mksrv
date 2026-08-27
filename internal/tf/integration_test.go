// SPDX-License-Identifier: Apache-2.0

//go:build integration

// These tests require a real Terraform binary. CI runs them with
// `go test -tags=integration ./internal/tf/...` after installing the pinned
// version; locally, set MKSRV_TERRAFORM or put a matching terraform on PATH.
package tf_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fenandosr/mksrv/internal/tf"
)

func TestRunnerLifecycle(t *testing.T) {
	ctx := context.Background()

	execPath, err := tf.Locate(ctx)
	if err != nil {
		t.Fatalf("Locate() error = %v", err)
	}

	work := t.TempDir()
	source, err := os.ReadFile(filepath.Join("testdata", "fixture", "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "main.tf"), source, 0o600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	runner, err := tf.NewRunner(execPath, work, &logs)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	if err := runner.Init(ctx, false); err != nil {
		t.Fatalf("Init() error = %v\n%s", err, logs.String())
	}
	if err := runner.Validate(ctx); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	planPath := filepath.Join(work, "plan.bin")
	changed, err := runner.Plan(ctx, planPath)
	if err != nil {
		t.Fatalf("Plan() error = %v\n%s", err, logs.String())
	}
	if !changed {
		t.Fatal("Plan() reported no changes; expected the new output")
	}

	if err := runner.Apply(ctx, planPath); err != nil {
		t.Fatalf("Apply() error = %v\n%s", err, logs.String())
	}

	outputs, err := runner.Output(ctx)
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	var greeting string
	if err := json.Unmarshal(outputs["greeting"], &greeting); err != nil {
		t.Fatalf("decode greeting output: %v (raw %s)", err, outputs["greeting"])
	}
	if greeting != "hola" {
		t.Fatalf("greeting = %q, want %q", greeting, "hola")
	}
}
