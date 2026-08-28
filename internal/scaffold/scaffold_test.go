// SPDX-License-Identifier: Apache-2.0

package scaffold

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/schema"
)

func validParams() Params {
	return Params{
		Engine:     "dev",
		Region:     "us-east-1",
		RootDomain: "example.com",
		MgmtCIDR:   "203.0.113.4/32",
		ACMEEmail:  "ops@example.com",
	}
}

func TestGenerateProducesSchemaValidDeployment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	created, err := Generate(dir, validParams(), false)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	for _, want := range []string{".gitignore", ".mksrv/.gitkeep", "README.md", "deployment.yaml", "tenants/.gitkeep"} {
		if !slices.Contains(created, want) {
			t.Fatalf("created %v missing %q", created, want)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, issues, err := schema.New().ValidateYAML("deployment.v1.json", data)
	if err != nil {
		t.Fatalf("ValidateYAML() error = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("rendered deployment.yaml is not schema-valid: %#v", issues)
	}
}

func TestGenerateDerivesIdentityDomains(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Generate(dir, validParams(), false); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"keycloak_domain: auth.example.com", "headscale_domain: vpn.example.com", "bucket: mksrv-prod-tfstate"} {
		if !strings.Contains(text, want) {
			t.Fatalf("deployment.yaml missing %q:\n%s", want, text)
		}
	}
}

func TestGenerateRefusesOverwriteWithoutForce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Generate(dir, validParams(), false); err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	if _, err := Generate(dir, validParams(), false); err == nil {
		t.Fatal("expected an error overwriting an existing deployment.yaml")
	}
	if _, err := Generate(dir, validParams(), true); err != nil {
		t.Fatalf("forced Generate() error = %v", err)
	}
}

func TestValidateReportsMissingAndMalformed(t *testing.T) {
	t.Parallel()
	if err := (Params{}).WithDefaults().Validate(); err == nil {
		t.Fatal("expected missing-value error")
	}
	bad := validParams()
	bad.MgmtCIDR = "not-a-cidr"
	if err := bad.WithDefaults().Validate(); err == nil {
		t.Fatal("expected CIDR error")
	}
}
