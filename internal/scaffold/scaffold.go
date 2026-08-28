// SPDX-License-Identifier: Apache-2.0

// Package scaffold renders a new private mksrv workspace from embedded
// templates. It writes only synthetic placeholder structure; the operator
// edits the result before the first apply. See ADR 0009.
package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

//go:embed templates
var templatesFS embed.FS

// Params carries the values required to render deployment.yaml. Missing
// optional values are filled by WithDefaults; Validate reports missing
// required values.
type Params struct {
	Engine          string
	Env             string
	Region          string
	Profile         string
	RootDomain      string
	MgmtCIDR        string
	KeycloakDomain  string
	HeadscaleDomain string
	ACMEEmail       string
}

// WithDefaults returns a copy of p with derived and default values applied.
func (p Params) WithDefaults() Params {
	out := p
	out.Engine = strings.TrimSpace(out.Engine)
	if out.Engine == "" {
		out.Engine = "dev"
	}
	out.Env = strings.TrimSpace(out.Env)
	if out.Env == "" {
		out.Env = "prod"
	}
	out.RootDomain = strings.TrimSpace(strings.ToLower(out.RootDomain))
	if out.KeycloakDomain == "" && out.RootDomain != "" {
		out.KeycloakDomain = "auth." + out.RootDomain
	}
	if out.HeadscaleDomain == "" && out.RootDomain != "" {
		out.HeadscaleDomain = "vpn." + out.RootDomain
	}
	out.Region = strings.TrimSpace(out.Region)
	out.Profile = strings.TrimSpace(out.Profile)
	out.MgmtCIDR = strings.TrimSpace(out.MgmtCIDR)
	out.ACMEEmail = strings.TrimSpace(out.ACMEEmail)
	out.KeycloakDomain = strings.TrimSpace(strings.ToLower(out.KeycloakDomain))
	out.HeadscaleDomain = strings.TrimSpace(strings.ToLower(out.HeadscaleDomain))
	return out
}

// MissingRequired returns the names of required values that are still empty
// after defaults are applied.
func (p Params) MissingRequired() []string {
	var missing []string
	if p.Region == "" {
		missing = append(missing, "region")
	}
	if p.RootDomain == "" {
		missing = append(missing, "root-domain")
	}
	if p.MgmtCIDR == "" {
		missing = append(missing, "mgmt-cidr")
	}
	if p.ACMEEmail == "" {
		missing = append(missing, "acme-email")
	}
	return missing
}

// Validate checks that every value is present and well formed.
func (p Params) Validate() error {
	if missing := p.MissingRequired(); len(missing) > 0 {
		return fmt.Errorf("missing required values: %s", strings.Join(missing, ", "))
	}
	if p.MgmtCIDR != "auto" {
		if _, _, err := net.ParseCIDR(p.MgmtCIDR); err != nil {
			return fmt.Errorf("mgmt-cidr %q is not a valid CIDR (or \"auto\"): %w", p.MgmtCIDR, err)
		}
	}
	if !strings.Contains(p.ACMEEmail, "@") {
		return fmt.Errorf("acme-email %q is not an email address", p.ACMEEmail)
	}
	return nil
}

// Bucket is the derived Terraform state bucket name.
func (p Params) Bucket() string { return "mksrv-" + p.Env + "-tfstate" }

// LockTable is the derived Terraform state-lock DynamoDB table name.
func (p Params) LockTable() string { return "mksrv-" + p.Env + "-lock" }

// Generate renders a workspace into dir. It refuses to overwrite an existing
// deployment.yaml unless force is set. The returned slice lists the
// workspace-relative paths that were created, sorted.
func Generate(dir string, params Params, force bool) ([]string, error) {
	params = params.WithDefaults()
	if err := params.Validate(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve target directory: %w", err)
	}
	deploymentPath := filepath.Join(absolute, "deployment.yaml")
	if _, err := os.Stat(deploymentPath); err == nil && !force {
		return nil, fmt.Errorf("deployment.yaml already exists in %s; pass --force to overwrite", absolute)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect %s: %w", deploymentPath, err)
	}

	deployment, err := render("deployment.yaml.tmpl", params)
	if err != nil {
		return nil, err
	}
	readme, err := render("README.md.tmpl", params)
	if err != nil {
		return nil, err
	}
	gitignore, err := templatesFS.ReadFile("templates/gitignore")
	if err != nil {
		return nil, fmt.Errorf("read embedded gitignore: %w", err)
	}

	files := map[string][]byte{
		"deployment.yaml": deployment,
		"README.md":       readme,
		".gitignore":      gitignore,
		"tenants/.gitkeep": []byte(
			"# tenant documents live here: <id>.yaml and optional <id>.users.yaml\n"),
		".mksrv/.gitkeep": []byte(
			"# mksrv writes generated Terraform inputs, plans, outputs, and state here\n"),
	}

	created := make([]string, 0, len(files))
	for name, contents := range files {
		target := filepath.Join(absolute, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, contents, 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", target, err)
		}
		created = append(created, name)
	}
	sort.Strings(created)
	return created, nil
}

func render(name string, params Params) ([]byte, error) {
	raw, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return nil, fmt.Errorf("read embedded template %s: %w", name, err)
	}
	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse embedded template %s: %w", name, err)
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, params); err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return buffer.Bytes(), nil
}
