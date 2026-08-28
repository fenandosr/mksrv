// SPDX-License-Identifier: Apache-2.0

// Package headscale reconciles Headscale users and pre-authentication keys by
// running the headscale CLI inside the identity stack's container over the SSH
// transport. One Headscale user is created per tenant; nodes join with a
// short-lived, single-use pre-auth key.
package headscale

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fenandosr/mksrv/internal/ssh"
)

const container = "mksrv-headscale"

// Runner executes a command on the identity host. *ssh.Client satisfies it.
type Runner interface {
	Run(ctx context.Context, command string) (ssh.Result, error)
}

// Client runs headscale commands inside the identity stack's container.
type Client struct {
	runner Runner
}

// New binds a client to a command runner for the identity host.
func New(r Runner) *Client { return &Client{runner: r} }

func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	cmd := fmt.Sprintf("sudo podman exec %s headscale %s", container, strings.Join(quoted, " "))
	res, err := c.runner.Run(ctx, cmd)
	if err != nil {
		return res.Stdout, fmt.Errorf("headscale %s: %w", strings.Join(args, " "), err)
	}
	return res.Stdout, nil
}

// User is a headscale user record.
type User struct {
	ID   json.Number `json:"id"`
	Name string      `json:"name"`
}

// Users lists headscale users.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	out, err := c.run(ctx, "users", "list", "--output", "json")
	if err != nil {
		return nil, err
	}
	var users []User
	if err := json.Unmarshal([]byte(out), &users); err != nil {
		return nil, fmt.Errorf("parse users list: %w", err)
	}
	return users, nil
}

// EnsureUser creates the named user if absent and returns its id.
func (c *Client) EnsureUser(ctx context.Context, name string) (string, error) {
	if id, err := c.findUser(ctx, name); err != nil || id != "" {
		return id, err
	}
	if _, err := c.run(ctx, "users", "create", name); err != nil {
		return "", err
	}
	id, err := c.findUser(ctx, name)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("headscale user %q was created but is not listed", name)
	}
	return id, nil
}

func (c *Client) findUser(ctx context.Context, name string) (string, error) {
	users, err := c.Users(ctx)
	if err != nil {
		return "", err
	}
	for _, u := range users {
		if u.Name == name {
			return u.ID.String(), nil
		}
	}
	return "", nil
}

// PreAuthKey mints a pre-auth key for a user.
func (c *Client) PreAuthKey(ctx context.Context, userID string, ttl time.Duration, reusable bool) (string, error) {
	args := []string{"preauthkeys", "create", "--user", userID, "--expiration", ttl.String(), "--output", "json"}
	if reusable {
		args = append(args, "--reusable")
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	var key struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal([]byte(out), &key); err != nil {
		return "", fmt.Errorf("parse preauth key: %w", err)
	}
	if key.Key == "" {
		return "", fmt.Errorf("headscale returned an empty pre-auth key")
	}
	return key.Key, nil
}

// Node is a headscale node record (subset).
type Node struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Online    bool     `json:"online"`
	IPv4      string   `json:"ip_addresses"`
	GivenName string   `json:"given_name"`
	Tags      []string `json:"forced_tags"`
}

// Nodes lists registered nodes.
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	out, err := c.run(ctx, "nodes", "list", "--output", "json")
	if err != nil {
		return nil, err
	}
	var nodes []Node
	if err := json.Unmarshal([]byte(out), &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes list: %w", err)
	}
	return nodes, nil
}
