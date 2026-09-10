// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

// operatorAppRolePolicyHCL is the least-privilege policy the `mksrv tenant
// secret-id` command runs under: it can mint, list, look up and destroy
// SecretIDs for any `tenant-*` AppRole, and read their RoleIDs — nothing else.
// No KV, no Transit, no policy writes. `+` matches one path segment.
const operatorAppRolePolicyHCL = `path "auth/approle/role/tenant-+/secret-id" {
  capabilities = ["create", "update", "list"]
}
path "auth/approle/role/tenant-+/secret-id-accessor/lookup" {
  capabilities = ["update"]
}
path "auth/approle/role/tenant-+/secret-id-accessor/destroy" {
  capabilities = ["update"]
}
path "auth/approle/role/tenant-+/role-id" {
  capabilities = ["read"]
}
`

// baoLogin is `bao write -format=json auth/approle/login`.
type baoLogin struct {
	Auth struct {
		ClientToken string `json:"client_token"`
	} `json:"auth"`
}

// operatorBaoToken returns a short-lived token scoped to
// operatorAppRolePolicyHCL. The `mksrv-operator` policy + AppRole are created
// with the root token the first time (a one-off), then their RoleID/SecretID
// live in SSM and every later call authenticates with those — the human
// operator never handles the root token, and after setup neither does mksrv for
// this path.
func (f *fleet) operatorBaoToken(ctx context.Context, client *sshx.Client) (string, error) {
	rid, ridErr := f.resolver.Get(ctx, "/mksrv/{env}/openbao/operator_role_id")
	sid, sidErr := f.resolver.Get(ctx, "/mksrv/{env}/openbao/operator_secret_id")
	if ridErr == nil && sidErr == nil {
		if tok, err := baoAppRoleLogin(ctx, client, rid, sid); err == nil {
			return tok, nil
		}
	}

	// One-time setup with the root token.
	root, err := f.resolver.Get(ctx, "/mksrv/{env}/openbao/root_token")
	if err != nil {
		return "", fmt.Errorf("openbao root token: %w (run mksrv openbao bootstrap first)", err)
	}
	if _, err := client.RunInput(ctx,
		baoExec(root, "policy", "write", "mksrv-operator", "-"), []byte(operatorAppRolePolicyHCL)); err != nil {
		return "", fmt.Errorf("write mksrv-operator policy: %w", err)
	}
	if _, err := client.Run(ctx, baoExec(root,
		"write", "auth/approle/role/mksrv-operator",
		"token_policies=mksrv-operator", "token_ttl=15m", "token_max_ttl=1h",
		"secret_id_ttl=0", "secret_id_num_uses=0")); err != nil {
		return "", fmt.Errorf("write mksrv-operator approle: %w", err)
	}
	ridRes, err := client.Run(ctx, baoExec(root, "read", "-field=role_id", "auth/approle/role/mksrv-operator/role-id"))
	if err != nil {
		return "", fmt.Errorf("read mksrv-operator role-id: %w", err)
	}
	rid = strings.TrimSpace(ridRes.Stdout)
	sidRes, err := client.Run(ctx, baoExec(root, "write", "-field=secret_id", "-f", "auth/approle/role/mksrv-operator/secret-id"))
	if err != nil {
		return "", fmt.Errorf("mint mksrv-operator secret-id: %w", err)
	}
	sid = strings.TrimSpace(sidRes.Stdout)
	if _, err := f.resolver.EnsureString(ctx, "/mksrv/{env}/openbao/operator_role_id", rid); err != nil {
		return "", err
	}
	if err := f.resolver.Put(ctx, "/mksrv/{env}/openbao/operator_secret_id", sid); err != nil {
		return "", err
	}
	return baoAppRoleLogin(ctx, client, rid, sid)
}

// baoAppRoleLogin exchanges a RoleID/SecretID for a token. The SecretID goes
// over stdin, not the argv.
func baoAppRoleLogin(ctx context.Context, client *sshx.Client, roleID, secretID string) (string, error) {
	res, err := client.RunInput(ctx,
		baoExec("", "write", "-format=json", "auth/approle/login", "role_id="+roleID, "secret_id=-"),
		[]byte(secretID))
	if err != nil {
		return "", err
	}
	var out baoLogin
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil || out.Auth.ClientToken == "" {
		return "", fmt.Errorf("parse approle login: %w", err)
	}
	return out.Auth.ClientToken, nil
}

// tenantSecretIDOptions carries the `mksrv tenant secret-id` flags.
type tenantSecretIDOptions struct {
	Name    string
	WrapTTL time.Duration
	TTL     time.Duration
	NumUses int
	CIDRs   []string
	List    bool
	Revoke  string
}

// secretIDMintArgs builds the `bao write` argv that mints one wrapped SecretID
// for role (`auth/approle/role/tenant-<id>`).
func secretIDMintArgs(role string, o tenantSecretIDOptions) []string {
	meta := map[string]string{"issued_by": "mksrv"}
	if o.Name != "" {
		meta["name"] = o.Name
	}
	metaJSON, _ := json.Marshal(meta)
	args := []string{
		"write", "-format=json", fmt.Sprintf("-wrap-ttl=%s", o.WrapTTL),
		role + "/secret-id",
		"metadata=" + string(metaJSON),
	}
	if len(o.CIDRs) > 0 {
		args = append(args, "cidr_list="+strings.Join(o.CIDRs, ","), "token_bound_cidrs="+strings.Join(o.CIDRs, ","))
	}
	if o.TTL > 0 {
		args = append(args, "ttl="+strconv.Itoa(int(o.TTL.Seconds())))
	}
	if o.NumUses > 0 {
		args = append(args, "num_uses="+strconv.Itoa(o.NumUses))
	}
	return args
}

func (a *App) runTenantSecretID(ctx context.Context, printer ui.Printer, globals *globalOptions, id string, o tenantSecretIDOptions) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	t, ok := f.data.Tenants[id]
	if !ok {
		return &ExitError{Code: 2, Err: fmt.Errorf("unknown tenant %q", id)}
	}
	if !slices.Contains(t.Stacks, "openbao") {
		return &ExitError{Code: 2, Err: fmt.Errorf("tenant %q does not consume the openbao stack", id)}
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}
	if f.openbao.Leader == "" {
		return &ExitError{Code: 2, Err: fmt.Errorf("openbao is not bootstrapped (run mksrv openbao bootstrap)")}
	}
	leader, ok := f.byName[f.openbao.Leader]
	if !ok {
		return &ExitError{Code: 2, Err: fmt.Errorf("openbao leader %q is not a fleet host", f.openbao.Leader)}
	}

	client, err := sshx.Dial(ctx, leader.Target, f.knownHosts)
	if err != nil {
		return dialError(leader.Name, err)
	}
	defer client.Close()

	token, err := f.operatorBaoToken(ctx, client)
	if err != nil {
		return &ExitError{Code: 1, Err: err}
	}
	role := "auth/approle/role/tenant-" + id

	switch {
	case o.List:
		res, err := client.Run(ctx, baoExec(token, "list", "-format=json", role+"/secret-id"))
		if err != nil {
			printer.Info("tenant %s: no SecretIDs issued", id)
			return nil
		}
		var accessors []string
		_ = json.Unmarshal([]byte(res.Stdout), &accessors)
		if len(accessors) == 0 {
			printer.Info("tenant %s: no SecretIDs issued", id)
			return nil
		}
		for _, acc := range accessors {
			look, _ := client.Run(ctx, baoExec(token, "write", "-format=json",
				role+"/secret-id-accessor/lookup", "secret_id_accessor="+acc))
			var meta struct {
				Data struct {
					Metadata        map[string]string `json:"metadata"`
					CreationTime    string            `json:"creation_time"`
					LastUpdatedTime string            `json:"last_updated_time"`
					SecretIDNumUses int               `json:"secret_id_num_uses"`
					SecretIDTTL     int               `json:"secret_id_ttl"`
					TokenBoundCIDRs []string          `json:"token_bound_cidrs"`
				} `json:"data"`
			}
			_ = json.Unmarshal([]byte(look.Stdout), &meta)
			printer.Info("  %s  name=%q  created=%s  cidrs=%v",
				acc, meta.Data.Metadata["name"], meta.Data.CreationTime, meta.Data.TokenBoundCIDRs)
		}
		return nil

	case o.Revoke != "":
		if _, err := client.Run(ctx, baoExec(token, "write",
			role+"/secret-id-accessor/destroy", "secret_id_accessor="+o.Revoke)); err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("destroy SecretID %s: %w", o.Revoke, err)}
		}
		printer.Success("tenant %s: SecretID %s revoked", id, o.Revoke)
		return nil
	}

	// Mint a wrapped SecretID.
	res, err := client.Run(ctx, baoExec(token, secretIDMintArgs(role, o)...))
	if err != nil {
		return &ExitError{Code: 1, Err: fmt.Errorf("mint SecretID: %w", err)}
	}
	var wrapped struct {
		WrapInfo struct {
			Token    string `json:"token"`
			Accessor string `json:"accessor"`
			TTL      int    `json:"ttl"`
		} `json:"wrap_info"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &wrapped); err != nil || wrapped.WrapInfo.Token == "" {
		return &ExitError{Code: 1, Err: fmt.Errorf("parse wrapped SecretID: %w", err)}
	}
	ridRes, err := client.Run(ctx, baoExec(token, "read", "-field=role_id", role+"/role-id"))
	if err != nil {
		return &ExitError{Code: 1, Err: fmt.Errorf("read RoleID: %w", err)}
	}
	roleID := strings.TrimSpace(ridRes.Stdout)

	if printer.JSON {
		return printer.Encode(map[string]any{
			"tenant":            id,
			"role_id":           roleID,
			"wrapping_token":    wrapped.WrapInfo.Token,
			"wrapping_accessor": wrapped.WrapInfo.Accessor,
			"wrap_ttl_seconds":  wrapped.WrapInfo.TTL,
			"name":              o.Name,
		})
	}

	printer.Success("tenant %s: wrapped SecretID minted (TTL %s)", id, o.WrapTTL)
	printer.Info("")
	printer.Info("  role_id (not secret):  %s", roleID)
	printer.Info("  wrapping token:        %s", wrapped.WrapInfo.Token)
	printer.Info("")
	printer.Info("send the wrapping token on a side channel. The recipient unwraps it once:")
	printer.Info("  BAO_TOKEN=<wrapping token> bao unwrap")
	printer.Info("→ the SecretID. It does not expire; store it in the service's secret store.")
	return nil
}
