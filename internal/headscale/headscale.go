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

// CreateAPIKey mints a Headscale API key valid for ttl. The key is shown only
// once, at creation.
func (c *Client) CreateAPIKey(ctx context.Context, ttl time.Duration) (string, error) {
	out, err := c.run(ctx, "apikeys", "create", "--expiration", ttl.String(), "--output", "json")
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(out)
	// Older/newer headscale prints either a bare quoted string or an object.
	if strings.HasPrefix(trimmed, "\"") {
		var key string
		if err := json.Unmarshal([]byte(trimmed), &key); err == nil {
			return key, nil
		}
	}
	var obj struct {
		APIKey string `json:"apiKey"`
		Key    string `json:"key"`
	}
	if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
		if obj.APIKey != "" {
			return obj.APIKey, nil
		}
		if obj.Key != "" {
			return obj.Key, nil
		}
	}
	if trimmed != "" && !strings.ContainsAny(trimmed, "{}[]") {
		return trimmed, nil
	}
	return "", fmt.Errorf("could not parse headscale api key from %q", trimmed)
}

// Node is a headscale node record (subset).
type Node struct {
	ID     json.Number `json:"id"`
	Name   string      `json:"name"`
	Online bool        `json:"online"`
	IPs    []string    `json:"ip_addresses"`
}

// IPv4 returns the node's first IPv4 tailnet address, or "".
func (n Node) IPv4() string {
	for _, ip := range n.IPs {
		if strings.Count(ip, ".") == 3 && !strings.Contains(ip, ":") {
			return ip
		}
	}
	return ""
}

// SetPolicyFile loads the HuJSON ACL policy at containerPath (a path visible
// inside the Headscale container) into Headscale (policy.mode: database).
func (c *Client) SetPolicyFile(ctx context.Context, containerPath string) error {
	if _, err := c.run(ctx, "policy", "set", "-f", containerPath); err != nil {
		return fmt.Errorf("headscale policy set: %w", err)
	}
	return nil
}

// PolicyTenant is one tenant's input to Policy: its id plus any subnet routes
// its own mesh nodes are allowed to advertise.
type PolicyTenant struct {
	ID     string
	Routes []string
}

// Policy renders the tenant-isolation HuJSON ACL: fleet hosts reach everything,
// each tenant reaches its own devices (and its own advertised subnet routes)
// freely and the fleet on service ports only, and tenants cannot reach each
// other. Route approval itself (headscale nodes approve-routes) stays manual.
func Policy(tenants []PolicyTenant) string {
	const fleetPorts = "22,80,443,3000,3010-3019,5050,5432,6379,8090,8200,9090"
	rules := []string{
		fmt.Sprintf(`{ "action": "accept", "src": ["%s@"], "dst": ["%s@:*"] }`, fleetUser, fleetUser),
	}
	for _, t := range tenants {
		rules = append(rules,
			fmt.Sprintf(`{ "action": "accept", "src": ["%s@"], "dst": ["%s@:*"] }`, t.ID, t.ID),
			fmt.Sprintf(`{ "action": "accept", "src": ["%s@"], "dst": ["%s@:%s"] }`, t.ID, fleetUser, fleetPorts),
		)
		for _, cidr := range t.Routes {
			rules = append(rules,
				fmt.Sprintf(`{ "action": "accept", "src": ["%s@"], "dst": ["%s:*"] }`, t.ID, cidr),
			)
		}
	}
	return "{\n  \"acls\": [\n    " + strings.Join(rules, ",\n    ") + "\n  ]\n}\n"
}

// fleetUser is the Headscale user that owns the mksrv fleet hosts.
const fleetUser = "mksrv-fleet"

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
