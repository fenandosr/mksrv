// SPDX-License-Identifier: Apache-2.0

// Package deploy runs host bootstrap and (from later milestones) stack
// deployment over the SSH transport in internal/ssh.
package deploy

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"text/template"

	"github.com/fenandosr/mksrv/internal/ssh"
)

//go:embed scripts/bootstrap.sh.tmpl
var bootstrapTemplate string

// BootstrapVersion is bumped when the bootstrap script changes in a way that
// must re-run on already-provisioned hosts.
const BootstrapVersion = 10

// VolumeMount pairs a stack storage name with the EBS volume id backing it, so
// the bootstrap can match the NVMe disk by serial.
type VolumeMount struct {
	Name     string
	VolumeID string
}

// BootstrapParams controls the rendered bootstrap script.
type BootstrapParams struct {
	IsEdge        bool
	Timezone      string
	SwapMB        int
	DataVolumeID  string
	Volumes       []VolumeMount
	MarkerVersion int
}

// RenderBootstrap returns the bootstrap script for params.
func RenderBootstrap(params BootstrapParams) (string, error) {
	if params.MarkerVersion == 0 {
		params.MarkerVersion = BootstrapVersion
	}
	if params.Timezone == "" {
		params.Timezone = "Etc/UTC"
	}
	tmpl, err := template.New("bootstrap").Parse(bootstrapTemplate)
	if err != nil {
		return "", fmt.Errorf("parse bootstrap template: %w", err)
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, params); err != nil {
		return "", fmt.Errorf("render bootstrap script: %w", err)
	}
	return buffer.String(), nil
}

// Bootstrap renders and runs the bootstrap script on client. The script is
// idempotent; the returned Result carries its output.
func Bootstrap(ctx context.Context, client *ssh.Client, params BootstrapParams) (ssh.Result, error) {
	script, err := RenderBootstrap(params)
	if err != nil {
		return ssh.Result{}, err
	}
	return client.RunScript(ctx, script)
}
