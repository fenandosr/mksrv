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

const openbaoFile = ".mksrv/openbao.json"

// openbaoCluster is the recorded state of the OpenBao Raft cluster, written to
// .mksrv/openbao.json after `mksrv openbao bootstrap`.
type openbaoCluster struct {
	Leader  string        `json:"leader"`
	APIAddr string        `json:"api_addr"`
	Engines []string      `json:"engines"`
	Nodes   []openbaoNode `json:"nodes"`
}

type openbaoNode struct {
	Host  string `json:"host"`
	IP    string `json:"ip"`
	State string `json:"state"` // "leader" or "follower"
}

// baoInit is the JSON from `bao operator init -format=json`.
type baoInit struct {
	RecoveryKeysB64 []string `json:"recovery_keys_b64"`
	RootToken       string   `json:"root_token"`
}

// baoStatus is the subset of `bao status -format=json` we act on.
type baoStatus struct {
	Initialized bool `json:"initialized"`
	Sealed      bool `json:"sealed"`
}

// baoRaftPeers is `bao operator raft list-peers -format=json`.
type baoRaftPeers struct {
	Data struct {
		Config struct {
			Servers []struct {
				NodeID  string `json:"node_id"`
				Address string `json:"address"`
				Leader  bool   `json:"leader"`
				Voter   bool   `json:"voter"`
			} `json:"servers"`
		} `json:"config"`
	} `json:"data"`
}

func (f *fleet) loadOpenBao() {
	blob, err := os.ReadFile(filepath.Join(f.data.Root, filepath.FromSlash(openbaoFile)))
	if err != nil {
		return
	}
	_ = json.Unmarshal(blob, &f.openbao)
}

func (f *fleet) writeOpenBao(c openbaoCluster) error {
	blob, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.data.Root, filepath.FromSlash(openbaoFile)), append(blob, '\n'), 0o600)
}

func (a *App) newOpenBaoCommand(opts *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "openbao", Short: "Operate the OpenBao (secrets) cluster"}
	cmd.AddCommand(&cobra.Command{
		Use:   "bootstrap",
		Short: "Initialise the cluster, store recovery keys in SSM, and enable KV v2 + AppRole",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOpenBaoBootstrap(cmd.Context(), a.printer(opts), opts)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show the seal state, Raft peers, and enabled engines",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOpenBaoStatus(cmd.Context(), a.printer(opts), opts)
		},
	})
	return cmd
}

// baoExec builds a `podman exec ... bao <args>` command. token is optional.
func baoExec(token string, args ...string) string {
	b := strings.Builder{}
	b.WriteString("sudo podman exec -e BAO_ADDR=http://127.0.0.1:8200")
	if token != "" {
		b.WriteString(" -e BAO_TOKEN=" + quoteArg(token))
	}
	b.WriteString(" mksrv-openbao bao")
	for _, arg := range args {
		b.WriteString(" " + quoteArg(arg))
	}
	return b.String()
}

func (f *fleet) openbaoMembers() []hostTarget {
	var members []hostTarget
	for _, ht := range f.targets {
		if slices.Contains(ht.Host.Stacks, "openbao") {
			members = append(members, ht)
		}
	}
	return members
}

func (a *App) runOpenBaoBootstrap(ctx context.Context, printer ui.Printer, globals *globalOptions) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}

	members := f.openbaoMembers()
	if len(members) < 3 || len(members)%2 == 0 {
		return &ExitError{Code: 2, Err: fmt.Errorf("openbao cluster needs an odd number of hosts >= 3; found %d", len(members))}
	}

	client, err := sshx.Dial(ctx, members[0].Target, f.knownHosts)
	if err != nil {
		return dialError(members[0].Name, err)
	}
	defer client.Close()

	st, err := waitForBaoServer(ctx, printer, client)
	if err != nil {
		return &ExitError{Code: 1, Err: err}
	}

	if !st.Initialized {
		printer.Info("initialising the cluster (recovery-shares=5, recovery-threshold=3)")
		res, err := client.Run(ctx, baoExec("", "operator", "init",
			"-recovery-shares=5", "-recovery-threshold=3", "-format=json"))
		if err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("bao operator init: %w", err)}
		}
		var init baoInit
		if err := json.Unmarshal([]byte(res.Stdout), &init); err != nil || init.RootToken == "" {
			return &ExitError{Code: 1, Err: fmt.Errorf("parse init output: %w", err)}
		}
		keysJSON, _ := json.Marshal(init.RecoveryKeysB64)
		if err := f.resolver.Put(ctx, "/mksrv/{env}/openbao/recovery_keys", string(keysJSON)); err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("store recovery keys: %w", err)}
		}
		if err := f.resolver.Put(ctx, "/mksrv/{env}/openbao/root_token", init.RootToken); err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("store root token: %w", err)}
		}
		printer.Success("cluster initialised; recovery keys + root token written to SSM")
	} else {
		printer.Info("cluster already initialised")
	}

	rootToken, err := f.resolver.Get(ctx, "/mksrv/{env}/openbao/root_token")
	if err != nil {
		return &ExitError{Code: 1, Err: fmt.Errorf("read root token: %w (run bootstrap on a fresh cluster first)", err)}
	}

	// Auto-unseal (KMS) should bring every node up within a minute of start.
	if err := waitForBaoUnsealed(ctx, printer, f, members); err != nil {
		return &ExitError{Code: 1, Err: err}
	}

	cluster, err := readBaoRaft(ctx, client, rootToken, f, members)
	if err != nil {
		return &ExitError{Code: 1, Err: err}
	}
	printer.Success("raft cluster: leader %s, %d voters", cluster.Leader, len(cluster.Nodes))

	engines, err := ensureBaoEngines(ctx, printer, client, rootToken)
	if err != nil {
		return &ExitError{Code: 1, Err: err}
	}
	cluster.Engines = engines

	if err := f.writeOpenBao(cluster); err != nil {
		printer.Warn("could not write %s: %v", openbaoFile, err)
	}
	f.openbao = cluster

	printer.Info("API over the tailnet: http://<node-tailnet-ip>:8200  (login: bao login <root token from SSM /mksrv/%s/openbao/root_token>)", f.data.Deployment.Env)
	return nil
}

func (a *App) runOpenBaoStatus(ctx context.Context, printer ui.Printer, globals *globalOptions) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}
	members := f.openbaoMembers()
	if len(members) == 0 {
		return &ExitError{Code: 2, Err: fmt.Errorf("no host carries openbao")}
	}
	for _, m := range members {
		c, err := sshx.Dial(ctx, m.Target, f.knownHosts)
		if err != nil {
			printer.Warn("%s: %v", m.Name, err)
			continue
		}
		res, _ := c.Run(ctx, baoExec("", "status", "-format=json"))
		_ = c.Close()
		var st baoStatus
		if json.Unmarshal([]byte(res.Stdout), &st) != nil {
			printer.Warn("%s: unreachable", m.Name)
			continue
		}
		printer.Info("%-10s initialized=%v sealed=%v", m.Name, st.Initialized, st.Sealed)
	}
	if len(f.openbao.Engines) > 0 {
		printer.Info("engines: %s (recorded leader %s)", strings.Join(f.openbao.Engines, ", "), f.openbao.Leader)
	}
	return nil
}

// waitForBaoServer polls `bao status` on node[0] until the server responds
// (sealed or not).
func waitForBaoServer(ctx context.Context, printer ui.Printer, client *sshx.Client) (baoStatus, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		res, _ := client.Run(ctx, baoExec("", "status", "-format=json"))
		var st baoStatus
		if json.Unmarshal([]byte(res.Stdout), &st) == nil {
			return st, nil
		}
		if time.Now().After(deadline) {
			return baoStatus{}, fmt.Errorf("openbao server did not respond within the timeout")
		}
		printer.Info("waiting for the openbao server...")
		select {
		case <-ctx.Done():
			return baoStatus{}, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

// waitForBaoUnsealed polls every node until KMS auto-unseal has completed.
func waitForBaoUnsealed(ctx context.Context, printer ui.Printer, f *fleet, members []hostTarget) error {
	deadline := time.Now().Add(3 * time.Minute)
	for {
		sealed := 0
		for _, m := range members {
			c, err := sshx.Dial(ctx, m.Target, f.knownHosts)
			if err != nil {
				sealed++
				continue
			}
			res, _ := c.Run(ctx, baoExec("", "status", "-format=json"))
			_ = c.Close()
			var st baoStatus
			if json.Unmarshal([]byte(res.Stdout), &st) != nil || st.Sealed {
				sealed++
			}
		}
		if sealed == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d/%d openbao nodes still sealed after the timeout (check the KMS grant)", sealed, len(members))
		}
		printer.Info("waiting for KMS auto-unseal (%d/%d still sealed)...", sealed, len(members))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

// readBaoRaft polls `raft list-peers` until there is one leader and every node
// is a voter, then maps peers to fleet hosts by private IP.
func readBaoRaft(ctx context.Context, client *sshx.Client, token string, f *fleet, members []hostTarget) (openbaoCluster, error) {
	byIP := map[string]string{}
	for _, m := range members {
		byIP[f.outputs.Hosts[m.Name].PrivateIP] = m.Name
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		res, err := client.Run(ctx, baoExec(token, "operator", "raft", "list-peers", "-format=json"))
		if err == nil {
			var peers baoRaftPeers
			if json.Unmarshal([]byte(res.Stdout), &peers) == nil {
				servers := peers.Data.Config.Servers
				leaders, voters := 0, 0
				cluster := openbaoCluster{}
				for _, s := range servers {
					host := byIP[strings.Split(s.Address, ":")[0]]
					state := "follower"
					if s.Leader {
						leaders++
						state = "leader"
						cluster.Leader = host
						cluster.APIAddr = "http://" + strings.Split(s.Address, ":")[0] + ":8200"
					}
					if s.Voter {
						voters++
					}
					cluster.Nodes = append(cluster.Nodes, openbaoNode{Host: host, IP: strings.Split(s.Address, ":")[0], State: state})
				}
				if leaders == 1 && voters == len(members) && len(servers) == len(members) {
					return cluster, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return openbaoCluster{}, fmt.Errorf("raft cluster did not converge (one leader, %d voters) within the timeout", len(members))
		}
		select {
		case <-ctx.Done():
			return openbaoCluster{}, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

// ensureBaoEngines enables KV v2 at kv/ and the AppRole auth method, idempotently.
func ensureBaoEngines(ctx context.Context, printer ui.Printer, client *sshx.Client, token string) ([]string, error) {
	secretsRes, err := client.Run(ctx, baoExec(token, "secrets", "list", "-format=json"))
	if err != nil {
		return nil, fmt.Errorf("list secrets engines: %w", err)
	}
	var mounts map[string]any
	_ = json.Unmarshal([]byte(secretsRes.Stdout), &mounts)
	if _, ok := mounts["kv/"]; !ok {
		if _, err := client.Run(ctx, baoExec(token, "secrets", "enable", "-path=kv", "-version=2", "kv")); err != nil {
			return nil, fmt.Errorf("enable kv v2: %w", err)
		}
		printer.Success("enabled KV v2 at kv/")
	}

	authRes, err := client.Run(ctx, baoExec(token, "auth", "list", "-format=json"))
	if err != nil {
		return nil, fmt.Errorf("list auth methods: %w", err)
	}
	var auths map[string]any
	_ = json.Unmarshal([]byte(authRes.Stdout), &auths)
	if _, ok := auths["approle/"]; !ok {
		if _, err := client.Run(ctx, baoExec(token, "auth", "enable", "approle")); err != nil {
			return nil, fmt.Errorf("enable approle: %w", err)
		}
		printer.Success("enabled AppRole auth")
	}

	// Transit: encryption-as-a-service for PII columns. Per-tenant keys live at
	// transit/keys/<id> and are created by `mksrv tenant apply`.
	if _, ok := mounts["transit/"]; !ok {
		if _, err := client.Run(ctx, baoExec(token, "secrets", "enable", "transit")); err != nil {
			return nil, fmt.Errorf("enable transit: %w", err)
		}
		printer.Success("enabled Transit at transit/")
	}
	return []string{"kv", "approle", "transit"}, nil
}
