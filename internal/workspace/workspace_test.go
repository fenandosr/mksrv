// SPDX-License-Identifier: Apache-2.0

package workspace

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateExampleWorkspace(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(filepath.Join("..", "..", "examples", "workspace"))
	_, report, err := Validate(context.Background(), root, ValidateOptions{RunningVersion: "dev"})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !report.Valid {
		t.Fatalf("report invalid: %#v", report.Issues)
	}
	if report.Hosts != 2 || report.Tenants != 1 || report.Users != 2 || report.CatalogStacks != 8 {
		t.Fatalf("counts = hosts:%d tenants:%d users:%d stacks:%d", report.Hosts, report.Tenants, report.Users, report.CatalogStacks)
	}
}

func TestValidateRejectsCloudOnlyStackOnExistingHost(t *testing.T) {
	t.Parallel()
	root := copyExample(t)
	deploymentPath := filepath.Join(root, "deployment.yaml")
	data, err := os.ReadFile(deploymentPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), "stacks: [database, files, analytics, monitor]", "stacks: [base, database, files, analytics, monitor]", 1)
	if err := os.WriteFile(deploymentPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	_, report, err := Validate(context.Background(), root, ValidateOptions{RunningVersion: "dev"})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if report.Valid {
		t.Fatal("expected invalid report")
	}
	assertIssueCode(t, report, "stack.target")
}

func TestValidateRejectsUnknownStack(t *testing.T) {
	t.Parallel()
	root := copyExample(t)
	deploymentPath := filepath.Join(root, "deployment.yaml")
	data, err := os.ReadFile(deploymentPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), "stacks: [database, files, analytics, monitor]", "stacks: [database, files, analytics, monitor, mystery]", 1)
	if err := os.WriteFile(deploymentPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	_, report, err := Validate(context.Background(), root, ValidateOptions{RunningVersion: "dev"})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	assertIssueCode(t, report, "stack.unknown")
}

func TestDiscoverWalksUp(t *testing.T) {
	t.Parallel()
	root := copyExample(t)
	nested := filepath.Join(root, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(nested, "")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got != root {
		t.Fatalf("Discover() = %q, want %q", got, root)
	}
}

func TestValidateRejectsReservedForwardID(t *testing.T) {
	t.Parallel()
	root := copyExample(t)
	patchTenant(t, root, "id: login\n    label:", "id: database\n    label:")
	report := revalidate(t, root)
	if report.Valid {
		t.Fatal("expected invalid report")
	}
	assertIssueCode(t, report, "forward.reserved")
}

func TestValidateRejectsDNSWithoutZone(t *testing.T) {
	t.Parallel()
	root := copyExample(t)
	patchTenant(t, root, "dns_override:\n  provider: route53\n  zone_id: ZEXAMPLEACMEZONEID\n", "")
	report := revalidate(t, root)
	if report.Valid {
		t.Fatal("expected invalid report")
	}
	assertIssueCode(t, report, "tenant.dns.no_zone")
}

func TestValidateRejectsBadMeshRoute(t *testing.T) {
	t.Parallel()
	root := copyExample(t)
	patchTenant(t, root, "- 10.90.0.0/24", "- 10.90.0.0/99")
	report := revalidate(t, root)
	if report.Valid {
		t.Fatal("expected invalid report")
	}
	assertIssueCode(t, report, "tenant.mesh_route")
}

func patchTenant(t *testing.T, root, old, new string) {
	t.Helper()
	p := filepath.Join(root, "tenants", "acme.yaml")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("acme.yaml missing %q", old)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(data), old, new, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func revalidate(t *testing.T, root string) Report {
	t.Helper()
	_, report, err := Validate(context.Background(), root, ValidateOptions{RunningVersion: "dev"})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	return report
}

func copyExample(t *testing.T) string {
	t.Helper()
	source := filepath.Clean(filepath.Join("..", "..", "examples", "workspace"))
	destination := t.TempDir()
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	}); err != nil {
		t.Fatalf("copy example: %v", err)
	}
	return destination
}

func assertIssueCode(t *testing.T, report Report, code string) {
	t.Helper()
	for _, issue := range report.Issues {
		if issue.Code == code {
			return
		}
	}
	t.Fatalf("issue code %q not found in %#v", code, report.Issues)
}
