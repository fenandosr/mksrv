// SPDX-License-Identifier: Apache-2.0

// Package workspace discovers, loads, and validates private mksrv workspaces.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/schema"
)

// ErrNotFound means no workspace marker was found while walking upward.
var ErrNotFound = errors.New("mksrv workspace not found")

// Issue is one workspace finding.
type Issue struct {
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`
	Path     string `json:"path,omitempty"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// Report is the machine-readable result of workspace validation.
type Report struct {
	Valid         bool    `json:"valid"`
	Workspace     string  `json:"workspace"`
	FilesChecked  int     `json:"files_checked"`
	Hosts         int     `json:"hosts"`
	Tenants       int     `json:"tenants"`
	Users         int     `json:"users"`
	CatalogStacks int     `json:"catalog_stacks"`
	Issues        []Issue `json:"issues"`
}

// Data is a fully decoded workspace.
type Data struct {
	Root        string
	Deployment  model.Deployment
	Tenants     map[string]model.Tenant
	TenantFiles map[string]string
	Users       map[string]model.UsersFile
	UsersFiles  map[string]string
	Lock        *model.Lock
	Catalog     map[string]model.Stack
}

// ValidateOptions controls semantic engine-version checks.
type ValidateOptions struct {
	RunningVersion string
	AllowDowngrade bool
}

// Discover resolves a workspace root. Explicit paths take precedence over
// MKSRV_WORKSPACE; otherwise discovery walks upward from start.
func Discover(start, explicit string) (string, error) {
	candidate := strings.TrimSpace(explicit)
	if candidate == "" {
		candidate = strings.TrimSpace(os.Getenv("MKSRV_WORKSPACE"))
	}
	if candidate != "" {
		return resolveCandidate(candidate)
	}
	if strings.TrimSpace(start) == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}
	absolute, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve discovery start: %w", err)
	}
	if info, statErr := os.Stat(absolute); statErr == nil && !info.IsDir() {
		absolute = filepath.Dir(absolute)
	}
	for {
		if isWorkspaceRoot(absolute) {
			return absolute, nil
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			break
		}
		absolute = parent
	}
	return "", fmt.Errorf("%w: no deployment.yaml or .mksrv directory found from %s upward", ErrNotFound, start)
}

func resolveCandidate(candidate string) (string, error) {
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path %q: %w", candidate, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect workspace path %q: %w", candidate, err)
	}
	if !info.IsDir() {
		if filepath.Base(absolute) != "deployment.yaml" {
			return "", fmt.Errorf("workspace path %q is a file but is not deployment.yaml", candidate)
		}
		absolute = filepath.Dir(absolute)
	}
	if !isWorkspaceRoot(absolute) {
		return "", fmt.Errorf("%w at %s: deployment.yaml is missing", ErrNotFound, absolute)
	}
	return absolute, nil
}

func isWorkspaceRoot(root string) bool {
	if info, err := os.Stat(filepath.Join(root, "deployment.yaml")); err == nil && !info.IsDir() {
		return true
	}
	if info, err := os.Stat(filepath.Join(root, ".mksrv")); err == nil && info.IsDir() {
		return true
	}
	return false
}

// Validate loads the workspace, enforces embedded schemas, and applies semantic checks.
func Validate(ctx context.Context, root string, options ValidateOptions) (Data, Report, error) {
	if err := ctx.Err(); err != nil {
		return Data{}, Report{}, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Data{}, Report{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	validator := schema.New()
	catalog, err := engine.Catalog(validator)
	if err != nil {
		return Data{}, Report{}, fmt.Errorf("load embedded stack catalog: %w", err)
	}
	data := Data{
		Root:        absolute,
		Tenants:     make(map[string]model.Tenant),
		TenantFiles: make(map[string]string),
		Users:       make(map[string]model.UsersFile),
		UsersFiles:  make(map[string]string),
		Catalog:     catalog,
	}
	report := Report{
		Workspace:     absolute,
		CatalogStacks: len(catalog),
		Issues:        make([]Issue, 0),
	}

	deploymentOK, err := validateAndDecodeFile(validator, filepath.Join(absolute, "deployment.yaml"), "deployment.v1.json", &data.Deployment, &report)
	if err != nil {
		return Data{}, Report{}, err
	}

	tenantPaths, err := filepath.Glob(filepath.Join(absolute, "tenants", "*.yaml"))
	if err != nil {
		return Data{}, Report{}, fmt.Errorf("list tenant files: %w", err)
	}
	sort.Strings(tenantPaths)
	for _, tenantPath := range tenantPaths {
		if strings.HasSuffix(tenantPath, ".users.yaml") {
			continue
		}
		var tenant model.Tenant
		ok, err := validateAndDecodeFile(validator, tenantPath, "tenant.v1.json", &tenant, &report)
		if err != nil {
			return Data{}, Report{}, err
		}
		if !ok {
			continue
		}
		if previous, exists := data.TenantFiles[tenant.ID]; exists {
			addIssue(&report, Issue{Severity: "error", File: relative(absolute, tenantPath), Path: "$.id", Code: "tenant.id.duplicate", Message: fmt.Sprintf("tenant id %q also appears in %s", tenant.ID, previous)})
			continue
		}
		data.Tenants[tenant.ID] = tenant
		data.TenantFiles[tenant.ID] = relative(absolute, tenantPath)
	}

	for _, usersPath := range tenantPaths {
		if !strings.HasSuffix(usersPath, ".users.yaml") {
			continue
		}
		var users model.UsersFile
		ok, err := validateAndDecodeFile(validator, usersPath, "users.v1.json", &users, &report)
		if err != nil {
			return Data{}, Report{}, err
		}
		if !ok {
			continue
		}
		if previous, exists := data.UsersFiles[users.Tenant]; exists {
			addIssue(&report, Issue{Severity: "error", File: relative(absolute, usersPath), Path: "$.tenant", Code: "users.tenant.duplicate", Message: fmt.Sprintf("another users file for tenant %q exists at %s", users.Tenant, previous)})
			continue
		}
		data.Users[users.Tenant] = users
		data.UsersFiles[users.Tenant] = relative(absolute, usersPath)
		report.Users += len(users.Users)
	}

	lockPath := filepath.Join(absolute, "mksrv.lock")
	if _, statErr := os.Stat(lockPath); statErr == nil {
		var lock model.Lock
		if err := decodeYAMLFile(lockPath, &lock); err != nil {
			addIssue(&report, Issue{Severity: "error", File: relative(absolute, lockPath), Path: "$", Code: "lock.parse", Message: err.Error()})
		} else {
			data.Lock = &lock
			report.FilesChecked++
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Data{}, Report{}, fmt.Errorf("inspect mksrv.lock: %w", statErr)
	}

	if deploymentOK {
		normalizeImplicitStacks(&data.Deployment)
		semanticChecks(&data, &report, options)
	}
	report.Hosts = len(data.Deployment.Hosts)
	report.Tenants = len(data.Tenants)
	report.Valid = !hasErrors(report.Issues)
	return data, report, nil
}

func validateAndDecodeFile(validator *schema.Validator, filename, schemaName string, out any, report *Report) (bool, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			addIssue(report, Issue{Severity: "error", File: filepath.Base(filename), Path: "$", Code: "file.missing", Message: "required file is missing"})
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", filename, err)
	}
	report.FilesChecked++
	value, schemaIssues, err := validator.ValidateYAML(schemaName, contents)
	if err != nil {
		addIssue(report, Issue{Severity: "error", File: relative(report.Workspace, filename), Path: "$", Code: "yaml.parse", Message: err.Error()})
		return false, nil
	}
	for _, found := range schemaIssues {
		addIssue(report, Issue{Severity: "error", File: relative(report.Workspace, filename), Path: found.Path, Code: "schema." + found.Keyword, Message: found.Message})
	}
	if len(schemaIssues) > 0 {
		return false, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false, fmt.Errorf("encode validated %s: %w", filename, err)
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return false, fmt.Errorf("decode validated %s: %w", filename, err)
	}
	return true, nil
}

func decodeYAMLFile(filename string, out any) error {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(contents, out); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(filename), err)
	}
	return nil
}

func addIssue(report *Report, issue Issue) {
	report.Issues = append(report.Issues, issue)
	sort.SliceStable(report.Issues, func(i, j int) bool {
		if report.Issues[i].Severity != report.Issues[j].Severity {
			return report.Issues[i].Severity < report.Issues[j].Severity
		}
		if report.Issues[i].File != report.Issues[j].File {
			return report.Issues[i].File < report.Issues[j].File
		}
		if report.Issues[i].Path != report.Issues[j].Path {
			return report.Issues[i].Path < report.Issues[j].Path
		}
		return report.Issues[i].Code < report.Issues[j].Code
	})
}

func hasErrors(issues []Issue) bool {
	for _, issue := range issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}

func relative(root, filename string) string {
	value, err := filepath.Rel(root, filename)
	if err != nil {
		return filepath.ToSlash(filename)
	}
	return filepath.ToSlash(value)
}
