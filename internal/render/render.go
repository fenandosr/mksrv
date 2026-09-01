// SPDX-License-Identifier: Apache-2.0

// Package render turns a stack's template files into concrete host files. Every
// template file is executed with text/template; a file with no actions passes
// through unchanged. Output is keyed by the descriptor's destination path.
package render

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/fenandosr/mksrv/internal/model"
)

// HostView is the per-host data a template can reference.
type HostView struct {
	Name      string
	Role      string // "edge" when it carries base, otherwise "data"
	PublicIP  string
	PrivateIP string
	TailnetIP string
	MeshName  string
	Stacks    []string
}

// Endpoints are the shared operator FQDNs.
type Endpoints struct {
	Keycloak   string
	Headscale  string
	ConfigD    string
	RootDomain string
}

// Member is one fleet host carrying a stack, for cluster templates.
type Member struct {
	Name      string
	PrivateIP string
	TailnetIP string
}

// Context is the data passed to every stack template.
type Context struct {
	Env          string
	Timezone     string
	ACMEEmail    string
	Host         HostView
	Endpoints    Endpoints
	Images       map[string]string
	Deployed     []string            // stacks already deployed on this host
	Peers        map[string]string   // mksrv host name -> private VPC IP
	StackHosts   map[string]string   // stack name -> private VPC IP of the host carrying it
	StackMembers map[string][]Member // stack name -> every fleet host carrying it (sorted by Name)
	Retention    model.RetentionConfig
	Tenant       *model.Tenant
	Secrets      map[string]string
}

// Peer returns the private VPC IP of another fleet host, or "" if unknown.
func (c Context) Peer(host string) string { return c.Peers[host] }

// Volume returns the host mount path of a stack's dedicated EBS volume declared
// in its `storage:` block. The bootstrap mounts it there; the path is
// deterministic whether or not the volume is attached yet.
func (c Context) Volume(name string) string { return "/var/lib/mksrv/vol/" + name }

// StackIP returns the private VPC IP of the fleet host carrying the named
// stack, or "" when no host does. Used by cross-host templates (an Alloy
// collector reaching Loki, Prometheus scraping CrowdSec).
func (c Context) StackIP(stack string) string { return c.StackHosts[stack] }

// StackNodes returns every fleet host carrying the named stack (sorted by Name).
func (c Context) StackNodes(stack string) []Member { return c.StackMembers[stack] }

// StackPeers returns StackNodes minus the host this context renders for — the
// other members of a cluster stack.
func (c Context) StackPeers(stack string) []Member {
	all := c.StackMembers[stack]
	peers := make([]Member, 0, len(all))
	for _, m := range all {
		if m.Name != c.Host.Name {
			peers = append(peers, m)
		}
	}
	return peers
}

// HasStack reports whether the host carries the named stack.
func (c Context) HasStack(name string) bool {
	for _, s := range c.Host.Stacks {
		if s == name {
			return true
		}
	}
	return false
}

// IsDeployed reports whether the named stack is already deployed on this host.
func (c Context) IsDeployed(name string) bool {
	for _, s := range c.Deployed {
		if s == name {
			return true
		}
	}
	return false
}

// Image returns the pinned image for an app, or "" when unknown.
func (c Context) Image(app string) string { return c.Images[app] }

var funcs = template.FuncMap{
	"default": func(fallback, value string) string {
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return value
	},
	"join":  strings.Join,
	"lower": strings.ToLower,
}

// Stack renders every template declared by stack, reading sources from
// stacksRoot/<stack>/<src>. The result maps each descriptor destination path to
// its rendered bytes.
func Stack(stacksRoot string, stack model.Stack, ctx Context) (map[string][]byte, error) {
	out := make(map[string][]byte, len(stack.Templates))
	for _, tmplSpec := range stack.Templates {
		if tmplSpec.Scope == "per_tenant" && ctx.Tenant == nil {
			continue
		}
		srcPath := filepath.Join(stacksRoot, stack.Name, filepath.FromSlash(tmplSpec.Src))
		raw, err := os.ReadFile(srcPath)
		if err != nil {
			return nil, fmt.Errorf("read template %s: %w", tmplSpec.Src, err)
		}
		rendered, err := renderOne(tmplSpec.Src, string(raw), ctx)
		if err != nil {
			return nil, err
		}
		dst := tmplSpec.Dst
		if ctx.Tenant != nil {
			dst = strings.ReplaceAll(dst, "{tenant}", ctx.Tenant.ID)
		}
		out[dst] = rendered
	}
	return out, nil
}

// One renders a single template file (used for units mksrv manages outside the
// stack deploy loop, e.g. configd).
func One(templatePath string, ctx Context) ([]byte, error) {
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("read template %s: %w", templatePath, err)
	}
	return renderOne(filepath.Base(templatePath), string(raw), ctx)
}

func renderOne(name, body string, ctx Context) ([]byte, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Funcs(funcs).Parse(body)
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, ctx); err != nil {
		return nil, fmt.Errorf("render template %s: %w", name, err)
	}
	return buffer.Bytes(), nil
}

// SortedPaths returns the destination paths of a render result in a stable
// order (parents before children), which is the order they should be written.
func SortedPaths(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
