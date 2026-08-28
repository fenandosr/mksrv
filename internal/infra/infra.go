// SPDX-License-Identifier: Apache-2.0

// Package infra materializes Terraform inputs from a decoded workspace. The
// generated files live under <workspace>/.mksrv/infra and are consumed by the
// Terraform root that mksrv runs from the versioned engine cache.
package infra

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/workspace"
)

// Dir is the workspace-relative directory holding generated Terraform inputs.
const Dir = ".mksrv/infra"

const backendKeyDefault = "mksrv.tfstate"

type tfvars struct {
	Deployment model.Deployment        `json:"deployment"`
	Tenants    map[string]model.Tenant `json:"tenants"`
}

// WorkDir returns the absolute generated Terraform working directory for a
// workspace root.
func WorkDir(root string) string {
	return filepath.Join(root, filepath.FromSlash(Dir))
}

// Materialize writes mksrv.auto.tfvars.json and backend.tf.json into
// WorkDir(data.Root), creating the directory if needed. It returns the
// workspace-relative paths written, sorted.
func Materialize(data workspace.Data) ([]string, error) {
	dir := WorkDir(data.Root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	tenants := data.Tenants
	if tenants == nil {
		tenants = map[string]model.Tenant{}
	}
	documents := map[string]any{
		"mksrv.auto.tfvars.json": tfvars{Deployment: data.Deployment, Tenants: tenants},
		"backend.tf.json":        backendDocument(data.Deployment),
	}
	written := make([]string, 0, len(documents))
	for name, value := range documents {
		if err := writeJSON(filepath.Join(dir, name), value); err != nil {
			return nil, err
		}
		written = append(written, filepath.ToSlash(filepath.Join(Dir, name)))
	}
	sort.Strings(written)
	return written, nil
}

// BackendRegion is the region the S3 state backend uses: the explicit backend
// region, or the deployment region as a fallback.
func BackendRegion(d model.Deployment) string {
	if d.Backend.Region != "" {
		return d.Backend.Region
	}
	return d.AWS.Region
}

// BackendKey is the state object key, defaulting to mksrv.tfstate.
func BackendKey(d model.Deployment) string {
	if d.Backend.Key != "" {
		return d.Backend.Key
	}
	return backendKeyDefault
}

func backendDocument(d model.Deployment) map[string]any {
	s3 := map[string]any{
		"bucket":         d.Backend.Bucket,
		"key":            BackendKey(d),
		"region":         BackendRegion(d),
		"dynamodb_table": d.Backend.DynamoDBTable,
		"encrypt":        true,
	}
	if d.AWS.Profile != "" {
		s3["profile"] = d.AWS.Profile
	}
	return map[string]any{
		"terraform": map[string]any{
			"backend": map[string]any{
				"s3": s3,
			},
		},
	}
}

func writeJSON(path string, value any) error {
	blob, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	blob = append(blob, '\n')
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
