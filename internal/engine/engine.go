// SPDX-License-Identifier: Apache-2.0

// Package engine exposes the embedded Terraform, stack, and schema tree.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	engineassets "github.com/fenandosr/mksrv"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/schema"
)

// Catalog loads and validates every embedded stacks/<name>/stack.yaml file.
func Catalog(validator *schema.Validator) (map[string]model.Stack, error) {
	paths, err := fs.Glob(engineassets.FS, "stacks/*/stack.yaml")
	if err != nil {
		return nil, fmt.Errorf("list embedded stacks: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("embedded stack catalog is empty")
	}
	sort.Strings(paths)
	catalog := make(map[string]model.Stack, len(paths))
	for _, stackPath := range paths {
		data, err := engineassets.FS.ReadFile(stackPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", stackPath, err)
		}
		value, issues, err := validator.ValidateYAML("stack.v1.json", data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", stackPath, err)
		}
		if len(issues) > 0 {
			return nil, fmt.Errorf("validate %s: %s", stackPath, formatSchemaIssues(issues))
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", stackPath, err)
		}
		var stack model.Stack
		if err := json.Unmarshal(encoded, &stack); err != nil {
			return nil, fmt.Errorf("decode %s: %w", stackPath, err)
		}
		directoryName := filepath.Base(filepath.Dir(stackPath))
		if stack.Name != directoryName {
			return nil, fmt.Errorf("%s: stack name %q must match directory %q", stackPath, stack.Name, directoryName)
		}
		if _, exists := catalog[stack.Name]; exists {
			return nil, fmt.Errorf("duplicate stack name %q", stack.Name)
		}
		if err := validateDescriptor(stack); err != nil {
			return nil, fmt.Errorf("%s: %w", stackPath, err)
		}
		catalog[stack.Name] = stack
	}
	return catalog, nil
}

func validateDescriptor(stack model.Stack) error {
	apps := make(map[string]struct{}, len(stack.Apps))
	for _, app := range stack.Apps {
		if _, exists := apps[app.Name]; exists {
			return fmt.Errorf("duplicate app name %q", app.Name)
		}
		apps[app.Name] = struct{}{}
	}
	for _, check := range stack.Health {
		if _, exists := apps[check.App]; !exists {
			return fmt.Errorf("health check references unknown app %q", check.App)
		}
		if check.Type == "http" && check.Path == "" {
			return fmt.Errorf("HTTP health check for %q requires path", check.App)
		}
		if (check.Type == "http" || check.Type == "tcp") && check.Port == 0 {
			return fmt.Errorf("%s health check for %q requires port", check.Type, check.App)
		}
	}
	for _, template := range stack.Templates {
		if template.Scope == "per_tenant" && !stack.PerTenant {
			return fmt.Errorf("non-tenant stack cannot declare per_tenant template %q", template.Src)
		}
	}
	return nil
}

// CachePath returns the versioned engine extraction directory.
func CachePath(version string) (string, error) {
	cacheRoot := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if cacheRoot == "" {
		var err error
		cacheRoot, err = os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolve user cache directory: %w", err)
		}
	}
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	return filepath.Join(cacheRoot, "mksrv", "engine", filepath.Base(version)), nil
}

// Extract atomically materializes the embedded engine tree. Development builds
// are refreshed on every invocation; release versions reuse a completed cache.
func Extract(ctx context.Context, version string) (string, error) {
	destination, err := CachePath(version)
	if err != nil {
		return "", err
	}
	marker := filepath.Join(destination, ".complete")
	if version != "dev" {
		if _, err := os.Stat(marker); err == nil {
			return destination, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create engine cache parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".extract-*")
	if err != nil {
		return "", fmt.Errorf("create temporary engine directory: %w", err)
	}
	defer os.RemoveAll(temporary)

	for _, root := range []string{"infra", "stacks", "schemas"} {
		if err := copyTree(ctx, root, temporary); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(temporary, ".complete"), []byte(version+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write engine cache marker: %w", err)
	}
	if err := os.RemoveAll(destination); err != nil {
		return "", fmt.Errorf("replace engine cache: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		return "", fmt.Errorf("publish engine cache atomically: %w", err)
	}
	return destination, nil
}

func copyTree(ctx context.Context, root, destination string) error {
	return fs.WalkDir(engineassets.FS, root, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk embedded engine at %s: %w", sourcePath, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(destination, filepath.FromSlash(sourcePath))
		if entry.IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("create engine directory %s: %w", target, err)
			}
			return nil
		}
		data, err := engineassets.FS.ReadFile(sourcePath)
		if err != nil {
			return fmt.Errorf("read embedded engine file %s: %w", sourcePath, err)
		}
		mode := fs.FileMode(0o600)
		if strings.HasSuffix(sourcePath, ".sh") {
			mode = 0o700
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return fmt.Errorf("write engine file %s: %w", target, err)
		}
		return nil
	})
}

func formatSchemaIssues(issues []schema.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, fmt.Sprintf("%s: %s", issue.Path, issue.Message))
	}
	return strings.Join(parts, "; ")
}
