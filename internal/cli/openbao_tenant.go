// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/fenandosr/mksrv/internal/keycloak"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

// openbaoOIDCRedirect is the loopback callback `bao login -method=oidc` uses.
const openbaoOIDCRedirect = "http://localhost:8250/oidc/callback"

// oidcConfigArgs builds the `bao write auth/oidc-<id>/config ...` arguments.
func oidcConfigArgs(id, discoveryURL, clientSecret string) []string {
	return []string{
		"write", "auth/oidc-" + id + "/config",
		"oidc_discovery_url=" + discoveryURL,
		"oidc_client_id=" + openbaoClientID,
		"oidc_client_secret=" + clientSecret,
		"default_role=tenant-" + id,
	}
}

// oidcRoleArgs builds the `bao write auth/oidc-<id>/role/tenant-<id> ...`
// arguments: any authenticated member of the tenant realm gets the tenant
// policy (the realm boundary is the isolation).
func oidcRoleArgs(id string) []string {
	return []string{
		"write", "auth/oidc-" + id + "/role/tenant-" + id,
		"user_claim=sub",
		"allowed_redirect_uris=" + openbaoOIDCRedirect,
		"token_policies=tenant-" + id,
		"token_ttl=1h", "token_max_ttl=4h",
		"oidc_scopes=openid",
	}
}

// tenantPolicyHCL is the OpenBao policy for tenant id: full access to its own
// KV v2 subtree and Transit key, and nothing else.
func tenantPolicyHCL(id string) string {
	return fmt.Sprintf(`path "kv/data/tenants/%[1]s/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}
path "kv/metadata/tenants/%[1]s/*" {
  capabilities = ["read", "list", "delete"]
}
path "transit/encrypt/%[1]s" {
  capabilities = ["update"]
}
path "transit/decrypt/%[1]s" {
  capabilities = ["update"]
}
path "transit/rewrap/%[1]s" {
  capabilities = ["update"]
}
path "transit/datakey/plaintext/%[1]s" {
  capabilities = ["update"]
}
path "transit/keys/%[1]s" {
  capabilities = ["read"]
}
`, id)
}

type baoDataRoleID struct {
	Data struct {
		RoleID string `json:"role_id"`
	} `json:"data"`
}

type baoDataSecretID struct {
	Data struct {
		SecretID string `json:"secret_id"`
	} `json:"data"`
}

// provisionOpenBaoTenants reconciles, for every selected tenant that consumes
// the `openbao` stack: a `tenant-<id>` policy over `kv/tenants/<id>/*` and the
// tenant's Transit key, an AppRole bound to it (RoleID/SecretID in SSM), a
// Transit key for PII, and an OIDC auth mount wired to the tenant's Keycloak
// realm. It is idempotent and never regenerates a live SecretID.
func (f *fleet) provisionOpenBaoTenants(ctx context.Context, printer ui.Printer, kc *keycloak.Client, tenants []string) error {
	if f.openbaoMembers() == nil {
		return nil
	}
	if f.openbao.Leader == "" {
		printer.Warn("openbao not bootstrapped; skipping per-tenant secrets (run mksrv openbao bootstrap)")
		return nil
	}
	leader, ok := f.byName[f.openbao.Leader]
	if !ok {
		printer.Warn("openbao leader %q is not a fleet host; skipping per-tenant secrets", f.openbao.Leader)
		return nil
	}

	consumers := make([]string, 0, len(tenants))
	for _, id := range tenants {
		if slices.Contains(f.data.Tenants[id].Stacks, "openbao") {
			consumers = append(consumers, id)
		}
	}
	if len(consumers) == 0 {
		return nil
	}

	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}
	rootToken, err := f.resolver.Get(ctx, "/mksrv/{env}/openbao/root_token")
	if err != nil {
		return fmt.Errorf("openbao root token: %w (run mksrv openbao bootstrap first)", err)
	}

	client, err := sshx.Dial(ctx, leader.Target, f.knownHosts)
	if err != nil {
		return dialError(leader.Name, err)
	}
	defer client.Close()

	authRes, err := client.Run(ctx, baoExec(rootToken, "auth", "list", "-format=json"))
	if err != nil {
		return fmt.Errorf("openbao: list auth methods: %w", err)
	}
	var authMounts map[string]any
	_ = json.Unmarshal([]byte(authRes.Stdout), &authMounts)

	keycloakDomain := f.data.Deployment.Identity.KeycloakDomain

	for _, id := range consumers {
		role := "tenant-" + id
		if _, err := client.RunInput(ctx,
			baoExec(rootToken, "policy", "write", role, "-"),
			[]byte(tenantPolicyHCL(id)),
		); err != nil {
			return fmt.Errorf("openbao %s: write policy: %w", id, err)
		}
		if _, err := client.Run(ctx, baoExec(rootToken,
			"write", "auth/approle/role/"+role,
			"token_policies="+role,
			"token_ttl=1h", "token_max_ttl=4h",
			"secret_id_num_uses=0", "secret_id_ttl=0",
		)); err != nil {
			return fmt.Errorf("openbao %s: write approle: %w", id, err)
		}

		// Transit key for PII-column encryption. Non-convergent (same plaintext
		// -> different ciphertext), non-exportable, auto-rotated every 90 days.
		if _, err := client.Run(ctx, baoExec(rootToken,
			"write", "-f", "transit/keys/"+id,
			"type=aes256-gcm96", "auto_rotate_period=2160h",
		)); err != nil {
			return fmt.Errorf("openbao %s: write transit key: %w", id, err)
		}

		res, err := client.Run(ctx, baoExec(rootToken, "read", "-format=json", "auth/approle/role/"+role+"/role-id"))
		if err != nil {
			return fmt.Errorf("openbao %s: read role-id: %w", id, err)
		}
		var rid baoDataRoleID
		if err := json.Unmarshal([]byte(res.Stdout), &rid); err != nil || rid.Data.RoleID == "" {
			return fmt.Errorf("openbao %s: parse role-id: %w", id, err)
		}
		if _, err := f.resolver.EnsureString(ctx, "/mksrv/{env}/openbao/approle_"+id+"_role_id", rid.Data.RoleID); err != nil {
			return fmt.Errorf("openbao %s: store role-id: %w", id, err)
		}

		if _, err := f.resolver.Get(ctx, "/mksrv/{env}/openbao/approle_"+id+"_secret_id"); err != nil {
			sres, err := client.Run(ctx, baoExec(rootToken, "write", "-f", "-format=json", "auth/approle/role/"+role+"/secret-id"))
			if err != nil {
				return fmt.Errorf("openbao %s: mint secret-id: %w", id, err)
			}
			var sid baoDataSecretID
			if err := json.Unmarshal([]byte(sres.Stdout), &sid); err != nil || sid.Data.SecretID == "" {
				return fmt.Errorf("openbao %s: parse secret-id: %w", id, err)
			}
			if err := f.resolver.Put(ctx, "/mksrv/{env}/openbao/approle_"+id+"_secret_id", sid.Data.SecretID); err != nil {
				return fmt.Errorf("openbao %s: store secret-id: %w", id, err)
			}
		}

		// OIDC: humans log in with their Keycloak identity via
		// `bao login -method=oidc -path=oidc-<id>`.
		realm := tenantRealm(f.data.Tenants[id])
		clientSecret, err := kc.EnsureClient(ctx, realm, keycloak.ClientSpec{
			ClientID: openbaoClientID, Public: false, RedirectURIs: openbaoRedirectURIs,
		})
		if err != nil {
			return fmt.Errorf("openbao %s: keycloak client: %w", id, err)
		}
		if _, ok := authMounts["oidc-"+id+"/"]; !ok {
			if _, err := client.Run(ctx, baoExec(rootToken, "auth", "enable", "-path=oidc-"+id, "oidc")); err != nil {
				return fmt.Errorf("openbao %s: enable oidc: %w", id, err)
			}
		}
		discoveryURL := "https://" + keycloakDomain + "/realms/" + realm
		if _, err := client.Run(ctx, baoExec(rootToken, oidcConfigArgs(id, discoveryURL, clientSecret)...)); err != nil {
			return fmt.Errorf("openbao %s: oidc config: %w", id, err)
		}
		if _, err := client.Run(ctx, baoExec(rootToken, oidcRoleArgs(id)...)); err != nil {
			return fmt.Errorf("openbao %s: oidc role: %w", id, err)
		}

		printer.Success("tenant %s: openbao policy + approle + transit + oidc (realm %s)", id, realm)
	}
	return nil
}
