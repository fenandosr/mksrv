// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

const postgresFile = ".mksrv/postgres.json"

// postgresCluster is the recorded state of the Patroni cluster, written to
// .mksrv/postgres.json for consumers (a follow-up wires Keycloak/PostgREST to it).
type postgresCluster struct {
	Scope   string         `json:"scope"`
	Primary string         `json:"primary"`
	Nodes   []postgresNode `json:"nodes"`
}

type postgresNode struct {
	Host   string `json:"host"`
	IP     string `json:"ip"`
	Member string `json:"member"`
	Role   string `json:"role"`
}

// patroniMember is a row of `patronictl list -f json`.
type patroniMember struct {
	Cluster string `json:"Cluster"`
	Member  string `json:"Member"`
	Host    string `json:"Host"`
	Role    string `json:"Role"`
	State   string `json:"State"`
}

func (f *fleet) loadPostgres() {
	blob, err := os.ReadFile(filepath.Join(f.data.Root, filepath.FromSlash(postgresFile)))
	if err != nil {
		return
	}
	_ = json.Unmarshal(blob, &f.postgres)
}

func (f *fleet) writePostgres(c postgresCluster) error {
	blob, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.data.Root, filepath.FromSlash(postgresFile)), append(blob, '\n'), 0o600)
}

func (a *App) newPostgresCommand(opts *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "postgres", Short: "Operate the HA PostgreSQL (Patroni) cluster"}
	boot := &cobra.Command{
		Use:   "bootstrap",
		Short: "Wait for the Patroni cluster, record its state, and create the app database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switchover, _ := cmd.Flags().GetBool("switchover")
			return a.runPostgresBootstrap(cmd.Context(), a.printer(opts), opts, switchover)
		},
	}
	boot.Flags().Bool("switchover", false, "after the cluster is healthy, force a switchover to demonstrate failover")
	cmd.AddCommand(boot)
	return cmd
}

func (a *App) runPostgresBootstrap(ctx context.Context, printer ui.Printer, globals *globalOptions, switchover bool) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}

	var members []hostTarget
	for _, ht := range f.targets {
		if slices.Contains(ht.Host.Stacks, "postgres") {
			members = append(members, ht)
		}
	}
	if len(members) < 3 || len(members)%2 == 0 {
		return &ExitError{Code: 2, Err: fmt.Errorf("postgres cluster needs an odd number of hosts >= 3; found %d", len(members))}
	}

	client, err := sshx.Dial(ctx, members[0].Target, f.knownHosts)
	if err != nil {
		return dialError(members[0].Name, err)
	}
	defer client.Close()

	rows, err := waitForPatroni(ctx, printer, client, len(members))
	if err != nil {
		return &ExitError{Code: 1, Err: err}
	}

	scope := "mksrv-" + f.data.Deployment.Env
	cluster := postgresCluster{Scope: scope}
	byIP := map[string]hostTarget{}
	for _, m := range members {
		byIP[f.outputs.Hosts[m.Name].PrivateIP] = m
	}
	var leaderHost hostTarget
	for _, r := range rows {
		host := byIP[r.Host].Name
		cluster.Nodes = append(cluster.Nodes, postgresNode{Host: host, IP: r.Host, Member: r.Member, Role: r.Role})
		if strings.EqualFold(r.Role, "leader") {
			cluster.Primary = r.Host
			leaderHost = byIP[r.Host]
		}
	}
	if cluster.Primary == "" {
		return &ExitError{Code: 1, Err: fmt.Errorf("cluster has no leader")}
	}
	printer.Success("cluster %s: leader %s (%s), %d nodes", scope, leaderHost.Name, cluster.Primary, len(rows))

	if err := f.provisionAppDatabase(ctx, printer, leaderHost); err != nil {
		return &ExitError{Code: 1, Err: err}
	}

	if err := f.writePostgres(cluster); err != nil {
		printer.Warn("could not write %s: %v", postgresFile, err)
	}
	f.postgres = cluster

	ips := make([]string, 0, len(cluster.Nodes))
	for _, n := range cluster.Nodes {
		ips = append(ips, n.IP)
	}
	printer.Info("consumer DSN: host=%s port=5432 dbname=app target_session_attrs=read-write", strings.Join(ips, ","))

	if switchover {
		leaderClient, err := sshx.Dial(ctx, leaderHost.Target, f.knownHosts)
		if err != nil {
			return dialError(leaderHost.Name, err)
		}
		defer leaderClient.Close()
		printer.Info("forcing a switchover away from %s", leaderHost.Name)
		if _, err := leaderClient.Run(ctx, "sudo podman exec mksrv-patroni patronictl -c /etc/patroni/patroni.yml switchover --force"); err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("switchover: %w", err)}
		}
		if _, err := waitForPatroni(ctx, printer, client, len(members)); err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("cluster did not re-converge after switchover: %w", err)}
		}
		printer.Success("cluster re-converged after switchover")
	}
	return nil
}

// waitForPatroni polls `patronictl list` until there is one leader and the rest
// are replicas in a running/streaming state.
func waitForPatroni(ctx context.Context, printer ui.Printer, client *sshx.Client, want int) ([]patroniMember, error) {
	deadline := time.Now().Add(6 * time.Minute)
	for {
		res, err := client.Run(ctx, "sudo podman exec mksrv-patroni patronictl -c /etc/patroni/patroni.yml list -f json")
		if err == nil {
			var rows []patroniMember
			if json.Unmarshal([]byte(res.Stdout), &rows) == nil && len(rows) == want {
				leaders, healthy := 0, 0
				for _, r := range rows {
					if strings.EqualFold(r.Role, "leader") {
						leaders++
					}
					switch strings.ToLower(r.State) {
					case "running", "streaming":
						healthy++
					}
				}
				if leaders == 1 && healthy == want {
					return rows, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("patroni cluster did not become healthy within the timeout")
		}
		printer.Info("waiting for the patroni cluster...")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func (f *fleet) provisionAppDatabase(ctx context.Context, printer ui.Printer, leader hostTarget) error {
	super, err := f.resolver.Get(ctx, "/mksrv/{env}/postgres/superpass")
	if err != nil {
		return fmt.Errorf("postgres superuser password: %w (deploy postgres first)", err)
	}
	appPass, err := f.resolver.EnsureRandom(ctx, "/mksrv/{env}/postgres/app_password", 24)
	if err != nil {
		return err
	}
	client, err := sshx.Dial(ctx, leader.Target, f.knownHosts)
	if err != nil {
		return dialError(leader.Name, err)
	}
	defer client.Close()

	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	sql := strings.Join([]string{
		fmt.Sprintf(`SELECT format('CREATE ROLE %%I LOGIN PASSWORD %%L', 'app', %s) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app')\gexec`, q(appPass)),
		fmt.Sprintf(`ALTER ROLE app WITH LOGIN PASSWORD %s;`, q(appPass)),
		`SELECT format('CREATE DATABASE %I OWNER %I', 'app', 'app') WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'app')\gexec`,
		"",
	}, "\n")
	cmd := fmt.Sprintf(
		"sudo podman exec -e PGPASSWORD=%s -i mksrv-patroni psql -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d postgres",
		quoteArg(super),
	)
	if _, err := client.RunInput(ctx, cmd, []byte(sql)); err != nil {
		return fmt.Errorf("create app database on %s: %w", leader.Name, err)
	}
	printer.Success("database app and role app ready on the primary")
	return nil
}
