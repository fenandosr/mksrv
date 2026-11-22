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

// backupTokenRef is where the backup token is kept between runs.
const backupTokenRef = "/mksrv/{env}/openbao/backup_token"

// backupBaoToken returns a periodic token bound to the raft-snapshot-only policy
// that the backup job uses as BAO_TOKEN.
//
// The token is REUSED. It used to be minted again on every `tenant apply`; the
// old ones were never revoked (twenty piled up) and that churn was, by accident,
// the only thing that kept a live token on the backup host: the policy gives the
// token no renew-self, and a periodic token that is not renewed dies after its
// period (30 days). Now the stored token is renewed with the root token on each
// run, a new one is minted only when the stored one is gone or invalid, and then
// every other backup-only token is revoked.
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
	addr = "http://" + f.outputs.Hosts[leader.Name].PrivateIP + ":8200"

	if _, err := client.RunInput(ctx, baoExec(root, "policy", "write", "backup", "-"), []byte(backupPolicyHCL)); err != nil {
		return "", "", fmt.Errorf("write backup policy: %w", err)
	}

	// Reuse: renew the stored token (the token travels on stdin, never in argv).
	if stored, getErr := f.resolver.Get(ctx, backupTokenRef); getErr == nil && stored != "" {
		body, _ := json.Marshal(map[string]string{"token": stored})
		if res, renewErr := client.RunInput(ctx, baoExec(root, "write", "-format=json", "auth/token/renew", "-"), body); renewErr == nil && renewalSucceeded(res.Stdout, stored) {
			return stored, addr, nil
		}
		// Renewal failed (revoked, expired, unknown): fall through and mint.
	}

	res, err := client.Run(ctx, baoExec(root, "write", "-f", "-format=json",
		"auth/token/create", "policies=backup", "no_default_policy=true", "period=720h"))
	if err != nil {
		return "", "", fmt.Errorf("mint token: %w", err)
	}
	token, accessor, err := parseTokenAuth(res.Stdout)
	if err != nil {
		return "", "", err
	}
	if err := f.resolver.Put(ctx, backupTokenRef, token); err != nil {
		return "", "", fmt.Errorf("store backup token: %w", err)
	}
	f.revokeStaleBackupTokens(ctx, client, root, accessor)
	return token, addr, nil
}

// parseTokenAuth extracts the client token and its accessor from the JSON of a
// token create/renew response.
func parseTokenAuth(stdout string) (token, accessor string, err error) {
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
			Accessor    string `json:"accessor"`
		} `json:"auth"`
	}
	if json.Unmarshal([]byte(stdout), &out) != nil || out.Auth.ClientToken == "" {
		return "", "", fmt.Errorf("parse token response")
	}
	return out.Auth.ClientToken, out.Auth.Accessor, nil
}

// renewalSucceeded reports whether the JSON of an `auth/token/renew` response
// shows a live token: the same client token came back, or at least an accessor
// and a positive lease (some versions omit the token itself on renew). Anything
// else is treated as a failed renewal, and the caller mints a new token.
func renewalSucceeded(stdout, stored string) bool {
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			Accessor      string `json:"accessor"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	if json.Unmarshal([]byte(stdout), &out) != nil {
		return false
	}
	if out.Auth.ClientToken != "" {
		return out.Auth.ClientToken == stored
	}
	return out.Auth.Accessor != "" && out.Auth.LeaseDuration > 0
}

// staleBackupAccessors picks, from accessor -> policies, the accessors of tokens
// whose ONLY policy is `backup` and that are not the one to keep. A token that
// carries anything else is never touched.
func staleBackupAccessors(policiesByAccessor map[string][]string, keep string) []string {
	var stale []string
	for accessor, policies := range policiesByAccessor {
		if accessor != keep && len(policies) == 1 && policies[0] == "backup" {
			stale = append(stale, accessor)
		}
	}
	slices.Sort(stale)
	return stale
}

// revokeStaleBackupTokens revokes every backup-only token except `keep` (an
// accessor, which is not a secret). Best effort: a failure here leaves old tokens
// in place, it never blocks the backup configuration.
func (f *fleet) revokeStaleBackupTokens(ctx context.Context, client *sshx.Client, root, keep string) {
	list, err := client.Run(ctx, baoExec(root, "list", "-format=json", "auth/token/accessors"))
	if err != nil {
		return
	}
	var accessors []string
	if json.Unmarshal([]byte(list.Stdout), &accessors) != nil {
		return
	}
	policies := make(map[string][]string, len(accessors))
	for _, a := range accessors {
		res, err := client.Run(ctx, baoExec(root, "write", "-format=json", "auth/token/lookup-accessor", "accessor="+a))
		if err != nil {
			continue
		}
		var look struct {
			Data struct {
				Policies []string `json:"policies"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(res.Stdout), &look) == nil {
			policies[a] = look.Data.Policies
		}
	}
	for _, a := range staleBackupAccessors(policies, keep) {
		_, _ = client.Run(ctx, baoExec(root, "write", "auth/token/revoke-accessor", "accessor="+a))
	}
}

func (a *App) newBackupCommand(opts *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Run and inspect restic backups"}
	cmd.AddCommand(&cobra.Command{
		Use:     "run",
		Example: "  mksrv backup run   # take a snapshot now; see the result with: mksrv backup list",
		Short:   "Trigger a backup now and wait for it",
		Args:    cobra.NoArgs,
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
