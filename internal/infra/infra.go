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

	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/workspace"
)

// Dir is the workspace-relative directory holding generated Terraform inputs.
const Dir = ".mksrv/infra"

const backendKeyDefault = "mksrv.tfstate"

// WorkDir returns the absolute generated Terraform working directory for a
// workspace root.
func WorkDir(root string) string {
	return filepath.Join(root, filepath.FromSlash(Dir))
}

// VarsFile is the tfvars filename Materialize writes inside WorkDir.
const VarsFile = "mksrv.auto.tfvars.json"

// Materialize writes the Terraform variables file into WorkDir(data.Root),
// creating the directory if needed. It returns the absolute path written.
// Any extra keys are merged in at the top level (e.g. "ssh_public_key").
// Backend settings are passed to `terraform init` as flags (see BackendConfig),
// not written to a file.
func Materialize(data workspace.Data, extra map[string]any) (string, error) {
	dir := WorkDir(data.Root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	tenants := data.Tenants
	if tenants == nil {
		tenants = map[string]model.Tenant{}
	}
	document := map[string]any{
		"deployment": data.Deployment,
		"tenants":    tenants,
	}
	for key, value := range extra {
		document[key] = value
	}
	path := filepath.Join(dir, VarsFile)
	if err := writeJSON(path, document); err != nil {
		return "", err
	}
	return path, nil
}

// OutputsFile is the filename apply writes the Terraform outputs to.
const OutputsFile = "outputs.json"

// HostOutput is the connection data the Terraform root exposes per host.
type HostOutput struct {
	Provider     string            `json:"provider"`
	ManagementIP string            `json:"management_ip"`
	PrivateIP    string            `json:"private_ip"`
	PublicIP     string            `json:"public_ip"`
	InstanceID   string            `json:"instance_id"`
	EBSDevice    string            `json:"ebs_device"`
	DataVolumeID string            `json:"data_volume_id"`
	Volumes      map[string]string `json:"volumes"` // stack storage name -> EBS volume id
	AZ           string            `json:"az"`
}

// Outputs is the decoded outputs.json.
type Outputs struct {
	Hosts map[string]HostOutput `json:"hosts"`
}

// LoadOutputs reads and decodes <root>/.mksrv/infra/outputs.json.
func LoadOutputs(root string) (Outputs, error) {
	path := filepath.Join(WorkDir(root), OutputsFile)
	blob, err := os.ReadFile(path)
	if err != nil {
		return Outputs{}, fmt.Errorf("read %s: %w (run mksrv apply --infra-only first)", path, err)
	}
	var out Outputs
	if err := json.Unmarshal(blob, &out); err != nil {
		return Outputs{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

// BackendConfig returns the `-backend-config` key=value entries for
// `terraform init`, derived from deployment.backend.
func BackendConfig(d model.Deployment) []string {
	entries := []string{
		"bucket=" + d.Backend.Bucket,
		"key=" + BackendKey(d),
		"region=" + BackendRegion(d),
		"dynamodb_table=" + d.Backend.DynamoDBTable,
		"encrypt=true",
	}
	if d.AWS.Profile != "" {
		entries = append(entries, "profile="+d.AWS.Profile)
	}
	return entries
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
