// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

const backupEnvPath = "/var/lib/mksrv/stacks/backup/backup.env"

// backupPolicyHCL lets a token read a raft snapshot and nothing else.
const backupPolicyHCL = `path "sys/storage/raft/snapshot" {
  capabilities = ["read"]
}
`

func (f *fleet) backupHost() (hostTarget, bool) {
	for _, ht := range f.targets {
		if slices.Contains(ht.Host.Stacks, "backup") {
			return ht, true
		}
	}
	return hostTarget{}, false
}

func (f *fleet) resticRepo() string {
	return fmt.Sprintf("s3:s3.%s.amazonaws.com/mksrv-%s-backups",
		f.data.Deployment.AWS.Region, f.data.Deployment.Env)
}

// provisionBackup writes backup.env on the backup host: the restic repo URL and
// the (non-secret-store) inputs backup.sh needs — the cluster primary IP + super
// password, a raft-snapshot-scoped OpenBao token, the Keycloak admin password,
// and the realm / database lists. It is idempotent.
func (f *fleet) provisionBackup(ctx context.Context, printer ui.Printer, kcAdminPass string) error {
	host, ok := f.backupHost()
	if !ok {
		return nil
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}
	if _, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/backup/restic_password", 32); err != nil {
		return fmt.Errorf("restic password: %w", err)
	}

	env := map[string]string{
		"RESTIC_REPOSITORY": f.resticRepo(),
		"KC_URL":            "https://" + f.data.Deployment.Identity.KeycloakDomain,
		"KC_ADMIN_PASSWORD": kcAdminPass,
	}

	if pg, ok, err := f.pgConn(); err == nil && ok {
		ip := pg.pgAdmin // primary IP in cluster mode, the container name otherwise
		if strings.Contains(ip, ".") {
			env["PG_PRIMARY_IP"] = ip
			if super, err := f.resolver.Get(ctx, pg.superRef); err == nil {
				env["PG_SUPERPASS"] = super
			}
		}
	}

	var realms, dbs []string
	for _, id := range sortedTenantIDs(f.data.Tenants) {
		t := f.data.Tenants[id]
		realms = append(realms, tenantRealm(t))
		if slices.Contains(t.Stacks, "database") {
			dbs = append(dbs, "db_"+id)
		}
	}
	env["REALMS"] = strings.Join(realms, " ")
	env["DBS"] = strings.Join(dbs, " ")

	if tok, addr, err := f.backupBaoToken(ctx); err != nil {
		printer.Warn("openbao raft snapshot not configured: %v", err)
	} else if tok != "" {
		env["BAO_ADDR"] = addr
		env["BAO_TOKEN"] = tok
	}

	client, err := sshx.Dial(ctx, host.Target, f.knownHosts)
	if err != nil {
		return dialError(host.Name, err)
	}
	defer client.Close()

	keys := sortedKeys(env)
	var b strings.Builder
	b.WriteString("# rendered by `mksrv tenant apply` — do not edit\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, shellQuote(env[k]))
	}
	if err := client.WriteFileSudo(ctx, backupEnvPath, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write backup env: %w", err)
	}
	printer.Success("backup: env written to %s (%d realms, %d databases)", host.Name, len(realms), len(dbs))
	return nil
}

// backupBaoToken mints a periodic token bound to a raft-snapshot-only policy.
func (f *fleet) backupBaoToken(ctx context.Context) (token, addr string, err error) {
	if f.openbao.Leader == "" {
		return "", "", nil
	}
	leader, ok := f.byName[f.openbao.Leader]
	if !ok {
		return "", "", fmt.Errorf("openbao leader %q not a fleet host", f.openbao.Leader)
	}
	root, err := f.resolver.Get(ctx, "/mksrv/{env}/openbao/root_token")
	if err != nil {
		return "", "", fmt.Errorf("openbao root token: %w", err)
	}
	client, err := sshx.Dial(ctx, leader.Target, f.knownHosts)
	if err != nil {
		return "", "", err
	}
	defer client.Close()

	if _, err := client.RunInput(ctx, baoExec(root, "policy", "write", "backup", "-"), []byte(backupPolicyHCL)); err != nil {
		return "", "", fmt.Errorf("write backup policy: %w", err)
	}
	res, err := client.Run(ctx, baoExec(root, "write", "-f", "-format=json",
		"auth/token/create", "policies=backup", "no_default_policy=true", "period=720h"))
	if err != nil {
		return "", "", fmt.Errorf("mint token: %w", err)
	}
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if json.Unmarshal([]byte(res.Stdout), &out) != nil || out.Auth.ClientToken == "" {
		return "", "", fmt.Errorf("parse token response")
	}
	return out.Auth.ClientToken, "http://" + f.outputs.Hosts[leader.Name].PrivateIP + ":8200", nil
}

func (a *App) newBackupCommand(opts *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Run and inspect restic backups"}
	cmd.AddCommand(&cobra.Command{
		Use:   "run",
		Short: "Trigger a backup now and wait for it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runBackup(cmd.Context(), a.printer(opts), opts, "run")
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List restic snapshots in the repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runBackup(cmd.Context(), a.printer(opts), opts, "list")
		},
	})
	return cmd
}

func (a *App) runBackup(ctx context.Context, printer ui.Printer, globals *globalOptions, action string) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}
	host, ok := f.backupHost()
	if !ok {
		return &ExitError{Code: 2, Err: fmt.Errorf("no host carries the `backup` stack")}
	}
	client, err := sshx.Dial(ctx, host.Target, f.knownHosts)
	if err != nil {
		return dialError(host.Name, err)
	}
	defer client.Close()

	switch action {
	case "run":
		printer.Info("starting mksrv-backup.service on %s ...", host.Name)
		if _, err := client.Run(ctx, "sudo systemctl start mksrv-backup.service"); err != nil {
			_, _ = client.Run(ctx, "sudo journalctl -u mksrv-backup.service -n 40 --no-pager")
			return &ExitError{Code: 1, Err: fmt.Errorf("backup run failed: %w", err)}
		}
		res, _ := client.Run(ctx, "sudo journalctl -u mksrv-backup.service -n 20 --no-pager -o cat")
		printer.Info("%s", strings.TrimSpace(res.Stdout))
		printer.Success("backup complete")
	case "list":
		res, err := client.Run(ctx, "sudo podman run --rm --network host "+
			"--secret mksrv-backup-restic_password,type=env,target=RESTIC_PASSWORD "+
			"-e RESTIC_REPOSITORY="+quoteArg(f.resticRepo())+
			" docker.io/restic/restic:0.19.1 snapshots --tag mksrv")
		if err != nil {
			return &ExitError{Code: 1, Err: err}
		}
		printer.Info("%s", strings.TrimSpace(res.Stdout))
	}
	return nil
}

// shellQuote wraps a value in single quotes for an env file, escaping any
// embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
