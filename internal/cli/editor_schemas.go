// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/schema"
	"github.com/fenandosr/mksrv/internal/ui"
)

// Editor schemas (ADR 0045, phase 1). The schema an editor uses to complete and
// check a workspace's YAML is generated from the binary (engine.EffectiveSchema)
// into <workspace>/.mksrv/schemas/, so what it suggests always matches the
// installed mksrv. mksrv never edits a user's YAML: it only writes the generated
// files, and tells you the one-line header that connects a file to them.

// editorSchemaDir is where the generated schemas live, relative to the workspace.
const editorSchemaDir = ".mksrv/schemas"

// workspaceSchemas are the schemas a workspace's own files use (a stack
// descriptor belongs to the engine, not to a workspace).
var workspaceSchemas = []string{"deployment", "tenant", "users"}

func (a *App) newSchemaCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "schema NAME",
		Short: "Print the JSON Schema an editor should use for a workspace file",
		Long: `Print the effective JSON Schema for a file of the workspace: deployment
(deployment.yaml), tenant (tenants/<id>.yaml), users (tenants/<id>.users.yaml) or
stack (a stack descriptor). It is the schema 'mksrv validate' enforces, plus the
values only this binary knows (the stack catalog, suggested instance types) as
completions and a description on the main fields.

'mksrv init' and 'mksrv validate' write the same files under .mksrv/schemas/ and
'validate' tells you the header that connects a file to them:

  # yaml-language-server: $schema=.mksrv/schemas/deployment.json`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: engine.SchemaNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			catalog, err := engine.Catalog(schema.New())
			if err != nil {
				return err
			}
			data, err := engine.EffectiveSchema(args[0], catalog)
			if err != nil {
				return &ExitError{Code: 2, Err: err}
			}
			_, err = a.stdout.Write(data)
			return err
		},
	}
}

// refreshEditorSchemas writes the generated schemas under <root>/.mksrv/schemas/,
// touching a file only when its content changed. It returns the number written.
func refreshEditorSchemas(root string, catalog map[string]model.Stack) (int, error) {
	dir := filepath.Join(root, filepath.FromSlash(editorSchemaDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	written := 0
	for _, name := range workspaceSchemas {
		data, err := engine.EffectiveSchema(name, catalog)
		if err != nil {
			return written, err
		}
		path := filepath.Join(dir, name+".json")
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
			continue
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// schemaNameForFile says which schema governs a workspace file, by its path
// relative to the workspace root, or "" for a file that has none.
func schemaNameForFile(rel string) string {
	rel = filepath.ToSlash(rel)
	switch {
	case rel == "deployment.yaml":
		return "deployment"
	case strings.HasPrefix(rel, "tenants/") && strings.HasSuffix(rel, ".users.yaml"):
		return "users"
	case strings.HasPrefix(rel, "tenants/") && strings.HasSuffix(rel, ".yaml"):
		return "tenant"
	}
	return ""
}

// schemaHeader is the comment line that connects a file to its generated schema,
// with the path made relative to the file (a tenant file lives one level down).
func schemaHeader(rel string) string {
	name := schemaNameForFile(rel)
	if name == "" {
		return ""
	}
	up := strings.Repeat("../", strings.Count(filepath.ToSlash(filepath.Dir(rel)), "/")+boolToInt(filepath.Dir(rel) != "."))
	return "# yaml-language-server: $schema=" + up + editorSchemaDir + "/" + name + ".json"
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// filesWithoutSchemaHeader lists the workspace's YAML files that have no
// yaml-language-server header in their first lines, sorted.
func filesWithoutSchemaHeader(root string) []string {
	var missing []string
	candidates := []string{"deployment.yaml"}
	if tenants, err := filepath.Glob(filepath.Join(root, "tenants", "*.yaml")); err == nil {
		for _, t := range tenants {
			if rel, err := filepath.Rel(root, t); err == nil {
				candidates = append(candidates, filepath.ToSlash(rel))
			}
		}
	}
	for _, rel := range candidates {
		if schemaNameForFile(rel) == "" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		if !hasSchemaHeader(string(body)) {
			missing = append(missing, rel)
		}
	}
	sort.Strings(missing)
	return missing
}

// hasSchemaHeader reports whether a yaml-language-server schema comment appears
// among the first lines of a YAML document.
func hasSchemaHeader(body string) bool {
	for i, line := range strings.Split(body, "\n") {
		if i >= 8 {
			break
		}
		if strings.Contains(line, "yaml-language-server:") && strings.Contains(line, "$schema=") {
			return true
		}
	}
	return false
}

// editorSchemaHint refreshes the generated schemas and, for files that have no
// header, prints ONE line saying how to add it. Best effort: an editor aid must
// never fail a validation.
func editorSchemaHint(printer ui.Printer, root string, catalog map[string]model.Stack) {
	if printer.JSON || len(catalog) == 0 {
		return
	}
	if _, err := refreshEditorSchemas(root, catalog); err != nil {
		return
	}
	missing := filesWithoutSchemaHeader(root)
	if len(missing) == 0 {
		return
	}
	printer.Info("hint: %d file(s) have no editor schema header (completion and typo checks in your editor); add, for example, to %s: %s",
		len(missing), missing[0], schemaHeader(missing[0]))
}
