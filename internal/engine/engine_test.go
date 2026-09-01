// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fenandosr/mksrv/internal/schema"
)

func TestCatalog(t *testing.T) {
	t.Parallel()
	catalog, err := Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	for _, expected := range []string{"base", "identity", "mail", "database", "postgres", "cache", "logs", "security", "files", "analytics", "monitor"} {
		if _, exists := catalog[expected]; !exists {
			t.Errorf("catalog missing %q", expected)
		}
	}
}

func TestExtract(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	path, err := Extract(context.Background(), "dev")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	for _, expected := range []string{"infra/root/main.tf", "stacks/base/stack.yaml", "schemas/deployment.v1.json"} {
		if _, err := os.Stat(filepath.Join(path, expected)); err != nil {
			t.Errorf("stat %s: %v", expected, err)
		}
	}
}
