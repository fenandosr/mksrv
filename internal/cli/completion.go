// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fenandosr/mksrv/internal/engine"
	"github.com/fenandosr/mksrv/internal/schema"
	"github.com/fenandosr/mksrv/internal/workspace"
)

// Dynamic shell completion (ADR 0045, phase 2). A completer runs on every TAB, so it must be
// fast and must never print or fail loudly: with no workspace, or one that cannot be read,
// it offers nothing (and the shell falls back to files, or to silence). It never touches the
// network, AWS or a host: only the workspace's own files and the embedded catalog.

// completionWorkspace loads the workspace quietly. It returns nil when there is none or it
// cannot be read; a workspace that merely fails validation still completes what it has.
func (a *App) completionWorkspace(globals *globalOptions) *workspace.Data {
	root, err := workspace.Discover(defaultStartDirectory(), globals.Workspace)
	if err != nil {
		return nil
	}
	data, _, err := workspace.Validate(context.Background(), root, workspace.ValidateOptions{
		RunningVersion: fallback(a.build.Version, "dev"),
		AllowDowngrade: true,
	})
	if err != nil {
		return nil
	}
	return &data
}

// completeNames filters candidates by the typed prefix and drops names already on the
// command line (for variadic arguments).
func completeNames(candidates, already []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	skip := map[string]bool{}
	for _, n := range already {
		skip[n] = true
	}
	var out []string
	for _, c := range candidates {
		if !skip[c] && strings.HasPrefix(c, toComplete) {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// tenantIDsCompleter completes tenant ids. max is how many ids the command takes (0 = any
// number); past that it offers nothing.
func (a *App) tenantIDsCompleter(globals *globalOptions, max int) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if max > 0 && len(args) >= max {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		data := a.completionWorkspace(globals)
		if data == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return completeNames(sortedTenantIDs(data.Tenants), args, toComplete)
	}
}

// hostNamesCompleter completes the fleet's host names for `deploy`.
func (a *App) hostNamesCompleter(globals *globalOptions) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		data := a.completionWorkspace(globals)
		if data == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := make([]string, 0, len(data.Deployment.Hosts))
		for n := range data.Deployment.Hosts {
			names = append(names, n)
		}
		return completeNames(names, args, toComplete)
	}
}

// stackNamesCompleter completes `--stack` from the embedded catalog (it needs no workspace).
func stackNamesCompleter(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	catalog, err := engine.Catalog(schema.New())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(catalog))
	for n := range catalog {
		names = append(names, n)
	}
	// `--stack a,b`: complete the last element, keep the ones before it.
	prefix, last := "", toComplete
	if i := strings.LastIndex(toComplete, ","); i >= 0 {
		prefix, last = toComplete[:i+1], toComplete[i+1:]
	}
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, last) && !strings.Contains(","+strings.TrimSuffix(prefix, ",")+",", ","+n+",") {
			out = append(out, prefix+n)
		}
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// approleNamesCompleter completes `--approle` with the storage.approles entries of the tenant
// already typed as the command's argument.
func (a *App) approleNamesCompleter(globals *globalOptions) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		data := a.completionWorkspace(globals)
		if data == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		t, ok := data.Tenants[args[0]]
		if !ok || t.Storage == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var names []string
		for _, r := range t.Storage.AppRoles {
			names = append(names, r.Name)
		}
		return completeNames(names, nil, toComplete)
	}
}
