// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"slices"

	"github.com/fenandosr/mksrv/internal/model"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

// runMigrateVolume copies a stack's data from its legacy named podman volume
// (under the shared graphroot) onto the dedicated EBS volume the bootstrap
// mounted at /var/lib/mksrv/vol/<name>. Run it AFTER `mksrv deploy <host>
// --stack <stack>` has re-rendered the unit onto the bind mount (the container
// restarts on an empty dir; `--ignore-existing` keeps the ~1-minute of data
// written since). The old volume is left as a backup.
func (a *App) runMigrateVolume(ctx context.Context, printer ui.Printer, globals *globalOptions, hostName, stackName string, names []string) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	ht, ok := f.byName[hostName]
	if !ok {
		return &ExitError{Code: 2, Err: fmt.Errorf("unknown host %q", hostName)}
	}
	if !slices.Contains(ht.Host.Stacks, stackName) {
		return &ExitError{Code: 2, Err: fmt.Errorf("host %q does not carry stack %q", hostName, stackName)}
	}
	stack, ok := f.catalog[stackName]
	if !ok {
		return &ExitError{Code: 2, Err: fmt.Errorf("unknown stack %q", stackName)}
	}

	var todo []model.StackVolume
	for _, v := range stack.Storage {
		if v.From == "" {
			continue
		}
		if len(names) > 0 && !slices.Contains(names, v.Name) {
			continue
		}
		todo = append(todo, v)
	}
	if len(todo) == 0 {
		return &ExitError{Code: 2, Err: fmt.Errorf("stack %q has no migratable volumes (need a `from:` in its storage block)", stackName)}
	}

	client, err := sshx.Dial(ctx, ht.Target, f.knownHosts)
	if err != nil {
		return dialError(hostName, err)
	}
	defer client.Close()

	for _, v := range todo {
		src := "/var/lib/mksrv/containers/volumes/" + v.From + "/_data/"
		dst := "/var/lib/mksrv/vol/" + v.Name + "/"
		if _, err := client.Run(ctx, "test -d "+quoteArg(src)+" && test -d "+quoteArg(dst)); err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("%s: source %s or mount %s missing (apply + bootstrap first)", v.Name, src, dst)}
		}
		if v.Unit != "" {
			if _, err := client.Run(ctx, "sudo systemctl stop "+quoteArg(v.Unit)); err != nil {
				return &ExitError{Code: 1, Err: fmt.Errorf("stop %s: %w", v.Unit, err)}
			}
		}
		_, cpErr := client.Run(ctx, fmt.Sprintf("sudo rsync -aHAX --ignore-existing %s %s", quoteArg(src), quoteArg(dst)))
		if v.Unit != "" {
			if _, err := client.Run(ctx, "sudo systemctl start "+quoteArg(v.Unit)); err != nil {
				return &ExitError{Code: 1, Err: fmt.Errorf("restart %s after copy: %w", v.Unit, err)}
			}
		}
		if cpErr != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("copy %s: %w", v.Name, cpErr)}
		}
		printer.Success("migrated %s: %s -> %s (old volume %s kept as backup)", v.Name, v.From, dst, v.From)
	}
	return nil
}
