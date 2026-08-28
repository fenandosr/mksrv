// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/fenandosr/mksrv/internal/ssh"
)

//go:embed scripts/tailscale.container.tmpl
var tailscaleUnit string

// MeshImage is the tailscale container image mksrv runs on fleet hosts.
const MeshImage = "docker.io/tailscale/tailscale:v1.80.3"

// MeshParams configures a host's tailnet node.
type MeshParams struct {
	LoginServer string // https://vpn.<root_domain>
	AuthKey     string // one-use Headscale pre-auth key
	Hostname    string // tailnet node name
	Image       string
}

// JoinMesh installs and starts the tailscale Quadlet unit on client and waits
// for the node to report a tailnet address. It is idempotent: an already-joined
// host re-uses its state and the call is a no-op restart.
func JoinMesh(ctx context.Context, client *ssh.Client, params MeshParams) (string, error) {
	if params.Image == "" {
		params.Image = MeshImage
	}
	if params.AuthKey != "" {
		if _, err := client.RunInput(ctx,
			"sudo podman secret create --replace mksrv-mesh-authkey -",
			[]byte(params.AuthKey),
		); err != nil {
			return "", fmt.Errorf("push mesh auth key: %w", err)
		}
	}

	unit, err := renderText("tailscale.container", tailscaleUnit, params)
	if err != nil {
		return "", err
	}
	same, _ := remoteMatches(ctx, client, "/etc/containers/systemd/mksrv-tailscale.container", unit)
	if err := client.WriteFileSudo(ctx, "/etc/containers/systemd/mksrv-tailscale.container", unit, 0o644); err != nil {
		return "", err
	}
	if !same {
		if _, err := client.Run(ctx, "sudo systemctl daemon-reload"); err != nil {
			return "", err
		}
	}
	if _, err := client.Run(ctx, "sudo systemctl restart mksrv-tailscale.service"); err != nil {
		return "", fmt.Errorf("start tailscale: %w", err)
	}

	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		res, err := client.Run(ctx, "sudo podman exec mksrv-tailscale tailscale ip -4 2>/dev/null | head -1")
		if err == nil {
			if ip := strings.TrimSpace(res.Stdout); ip != "" {
				return ip, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("tailnet node did not come up: %w", lastErr)
	}
	return "", fmt.Errorf("tailnet node did not obtain an address within the timeout")
}

func renderText(name, body string, data any) ([]byte, error) {
	tmpl, err := template.New(name).Parse(body)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return buffer.Bytes(), nil
}
