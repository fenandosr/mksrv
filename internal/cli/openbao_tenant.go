// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fenandosr/mksrv/internal/keycloak"
	"github.com/fenandosr/mksrv/internal/model"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

// openbaoOIDCRedirect is the loopback callback `bao login -method=oidc` uses.
const openbaoOIDCRedirect = "http://localhost:8250/oidc/callback"

// openbaoTenantIDs returns every tenant consuming the `openbao` stack, sorted
// by id — the full roster, not just one `tenant apply` run's selection, so a
// partial apply never narrows the mksrv-operator policy's access to another
// tenant that just wasn't named this time (operatorAppRolePolicyHCL).
func openbaoTenantIDs(tenants map[string]model.Tenant) []string {
	var ids []string
	for id, t := range tenants {
		if slices.Contains(t.Stacks, "openbao") {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// oidcConfigArgs builds the `bao write auth/oidc-<id>/config ...` arguments.
// A bare `bao login` uses the dev role; admins pass `-role=tenant-<id>-admin`.
func oidcConfigArgs(id, discoveryURL, clientSecret string) []string {
	return []string{
		"write", "auth/oidc-" + id + "/config",
		"oidc_discovery_url=" + discoveryURL,
		"oidc_client_id=" + openbaoClientID,
		"oidc_client_secret=" + clientSecret,
		"default_role=tenant-" + id + "-dev",
	}
}

// oidcGroupRolePayload is the JSON body for a per-group OIDC role: only realm
// members whose `groups` claim contains one of boundGroups can assume it, and it
// grants exactly `policy`. Passed to `bao write <path> -` on stdin so the
// `bound_claims` map survives shell quoting.
func oidcGroupRolePayload(boundGroups []string, policy string) []byte {
	body := map[string]any{
		"user_claim":            "sub",
		"allowed_redirect_uris": openbaoOIDCRedirect,
		"bound_claims":          map[string]any{"groups": boundGroups},
		"token_policies":        policy,
		"token_ttl":             "1h",
		"token_max_ttl":         "4h",
		"oidc_scopes":           "openid",
	}
	b, _ := json.Marshal(body)
	return b
}

func oidcGroupRolePath(id, suffix string) string {
	return "auth/oidc-" + id + "/role/tenant-" + id + "-" + suffix
}

// meshHostFQDN is the tailnet name a fleet host is reachable at — the address
// anything on the mesh (a tenant's own enrolled machine included) uses.
func meshHostFQDN(env, host string) string {
	return fmt.Sprintf("%s-%s.%s.mksrv", env, host, env)
}

// tenantDBSecretFields returns the KV fields for a tenant's Postgres
// connection. On the distributed profile the target is the Patroni node list
// over the mesh (with target_session_attrs so writes always land on the
// leader); standalone keeps the single `mksrv-postgres` container name.
func tenantDBSecretFields(pg postgresCluster, env, id, password string) []string {
	host, suffix := "mksrv-postgres:5432", ""
	if len(pg.Nodes) > 0 {
		parts := make([]string, len(pg.Nodes))
		for i, n := range pg.Nodes {
			name := n.Host
			if name == "" {
				name = n.IP
			} else {
				name = meshHostFQDN(env, name)
			}
			parts[i] = name + ":5432"
		}
		host = strings.Join(parts, ",")
		suffix = "?target_session_attrs=read-write"
	}
	// The human login role is <id>_login (ADR 0026); the mirrored credential is
	// the same one in SSM tenant_<id>_password.
	return []string{
		"dbname=db_" + id,
		"username=" + id + "_login",
		"password=" + password,
		"hosts=" + host,
		"url=postgres://" + id + "_login:" + password + "@" + host + "/db_" + id + suffix,
	}
}

// tenantCacheSecretFields returns the KV fields for a tenant's Redis
// connection. cacheHost is the `cache` stack host's mesh name (or "" to fall
// back to the container name for a single-host/dev layout).
func tenantCacheSecretFields(cacheHost, id, password string) []string {
	host := cacheHost
	if host == "" {
		host = "mksrv-redis"
	}
	return []string{
		"host=" + host + ":6379",
		"username=" + id,
		"password=" + password,
		"url=redis://" + id + ":" + password + "@" + host + ":6379",
	}
}

// baoKVPasswordField pulls the current `password` value from a `bao kv get
// -format=json` document, or "" when absent.
func baoKVPasswordField(stdout string) string {
	var doc struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(stdout), &doc) != nil {
		return ""
	}
	return doc.Data.Data["password"]
}

// mirrorTenantSecret writes fields to kv/tenants/<id>/<name>, skipping the write
// when the stored `password` already matches (KV v2 keeps a version per write).
func mirrorTenantSecret(ctx context.Context, client *sshx.Client, token, id, name, password string, fields []string) error {
	path := "kv/tenants/" + id + "/" + name
	if cur, _ := client.Run(ctx, baoExec(token, "kv", "get", "-format=json", path)); baoKVPasswordField(cur.Stdout) == password {
		return nil
	}
	if _, err := client.Run(ctx, baoExec(token, append([]string{"kv", "put", path}, fields...)...)); err != nil {
		return fmt.Errorf("openbao %s: mirror %s secret: %w", id, name, err)
	}
	return nil
}

// tenantAdminPolicyHCL is the `admin` group's OpenBao policy: full control of
// the tenant's KV subtree and Transit key, including version destroy and key
// rotation.
func tenantAdminPolicyHCL(id string) string {
	return fmt.Sprintf(`path "kv/data/tenants/%[1]s/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}
path "kv/metadata/tenants/%[1]s/*" {
  capabilities = ["read", "list", "delete"]
}
path "kv/delete/tenants/%[1]s/*" {
  capabilities = ["update"]
}
path "kv/undelete/tenants/%[1]s/*" {
  capabilities = ["update"]
}
path "kv/destroy/tenants/%[1]s/*" {
  capabilities = ["update"]
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
path "transit/hmac/%[1]s" {
  capabilities = ["update"]
}
path "transit/datakey/plaintext/%[1]s" {
  capabilities = ["update"]
}
path "transit/keys/%[1]s" {
  capabilities = ["read", "list"]
}
path "transit/keys/%[1]s/rotate" {
  capabilities = ["update"]
}
path "transit/keys/%[1]s/config" {
  capabilities = ["update"]
}
`, id)
}

// tenantDevPolicyHCL is the `dev` group's policy (also the AppRole's, for
// services): read-only on the tenant KV subtree, read/write on the `dev/`
// sub-path, and, on the tenant's own Transit key, encrypt/decrypt, HMAC
// (blind-index columns for equality search over encrypted data), and
// datakey/plaintext (envelope encryption for bulk PII). Not rewrap, rotate, or
// key config — those stay `admin`. A more specific path wins, so the dev/* grant
// overrides the read-only base.
func tenantDevPolicyHCL(id string) string {
	return fmt.Sprintf(`path "kv/data/tenants/%[1]s/*" {
  capabilities = ["read", "list"]
}
path "kv/metadata/tenants/%[1]s/*" {
  capabilities = ["read", "list"]
}
path "kv/data/tenants/%[1]s/dev/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}
path "kv/metadata/tenants/%[1]s/dev/*" {
  capabilities = ["read", "list", "delete"]
}
path "transit/encrypt/%[1]s" {
  capabilities = ["update"]
}
path "transit/decrypt/%[1]s" {
  capabilities = ["update"]
}
path "transit/hmac/%[1]s" {
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

// tenantServicePolicyHCL is the narrowest tier: Transit encrypt/decrypt/hmac/
// datakey only, on the tenant's own key. No KV at all — a leaked SecretID
// bound to this policy can encrypt and decrypt the tenant's PII columns and
// nothing else (not even read-only access to the tenant's other secrets,
// unlike the `-dev` policy). For a tenant-owned production service (Django,
// Celery, …) that only needs to en/decrypt, not the broader access a human
// developer's AppRole gets. Requested live for `hg`'s pilot deployment: the
// existing `tenant-<id>` AppRole is bound to `-dev`, which also grants
// read-only KV over the tenant's whole subtree — more than a production
// service should hold.
func tenantServicePolicyHCL(id string) string {
	return fmt.Sprintf(`path "transit/encrypt/%[1]s" {
  capabilities = ["update"]
}
path "transit/decrypt/%[1]s" {
  capabilities = ["update"]
}
path "transit/hmac/%[1]s" {
  capabilities = ["update"]
}
path "transit/datakey/plaintext/%[1]s" {
  capabilities = ["update"]
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

	// Keep the mksrv-operator policy (operatorAppRolePolicyHCL, tenant_secretid.go)
	// current — one explicit block per tenant, generated from the FULL roster
	// (every openbao consumer, not just this run's `tenants`), so a partial
	// `tenant apply <id>` never narrows another tenant's already-granted
	// access. operatorBaoToken only writes it once, at first bootstrap, and
	// never touches the root token again after that by design, so a newly
	// added tenant (or the `-svc` tier) wouldn't otherwise reach an
	// already-bootstrapped cluster. Piggybacks on the root token this function
	// already holds for its own tenant policies; cheap and idempotent.
	if _, err := client.RunInput(ctx,
		baoExec(rootToken, "policy", "write", "mksrv-operator", "-"),
		[]byte(operatorAppRolePolicyHCL(openbaoTenantIDs(f.data.Tenants))),
	); err != nil {
		return fmt.Errorf("openbao: refresh mksrv-operator policy: %w", err)
	}

	authRes, err := client.Run(ctx, baoExec(rootToken, "auth", "list", "-format=json"))
	if err != nil {
		return fmt.Errorf("openbao: list auth methods: %w", err)
	}
	var authMounts map[string]any
	_ = json.Unmarshal([]byte(authRes.Stdout), &authMounts)

	keycloakDomain := f.data.Deployment.Identity.KeycloakDomain

	for _, id := range consumers {
		role := "tenant-" + id
		for name, hcl := range map[string]string{
			role + "-admin": tenantAdminPolicyHCL(id),
			role + "-dev":   tenantDevPolicyHCL(id),
		} {
			if _, err := client.RunInput(ctx,
				baoExec(rootToken, "policy", "write", name, "-"), []byte(hcl),
			); err != nil {
				return fmt.Errorf("openbao %s: write policy %s: %w", id, name, err)
			}
		}
		// Best-effort: drop the pre-RBAC single policy/role if still present.
		_, _ = client.Run(ctx, baoExec(rootToken, "policy", "delete", role))
		_, _ = client.Run(ctx, baoExec(rootToken, "delete", "auth/oidc-"+id+"/role/tenant-"+id))

		// The AppRole (services) gets the dev policy.
		if _, err := client.Run(ctx, baoExec(rootToken,
			"write", "auth/approle/role/"+role,
			"token_policies="+role+"-dev",
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

		// The `svc` tier (Transit only, no KV) for a tenant-owned production
		// service. Distinct AppRole from `role` above (which stays bound to
		// `-dev`) — `mksrv tenant secret-id <id> --service` mints from this one.
		// No bootstrap SecretID minted/stored here (unlike `role` above): mksrv
		// itself never needs this credential, only the tenant's service does,
		// and only via the wrapped, on-demand `secret-id` flow.
		//
		// Named svc-<id>, not tenant-<id>-svc: operatorAppRolePolicyHCL's `+`
		// glob only matches a wildcard at the end of a path segment (confirmed
		// live — a `tenant-+-svc` grant does not match `tenant-hg-svc`), so the
		// wildcard-friendly id has to be the suffix, same shape as tenant-<id>.
		svcRole := "svc-" + id
		if _, err := client.RunInput(ctx,
			baoExec(rootToken, "policy", "write", svcRole, "-"), []byte(tenantServicePolicyHCL(id)),
		); err != nil {
			return fmt.Errorf("openbao %s: write policy %s: %w", id, svcRole, err)
		}
		if _, err := client.Run(ctx, baoExec(rootToken,
			"write", "auth/approle/role/"+svcRole,
			"token_policies="+svcRole,
			"token_ttl=1h", "token_max_ttl=4h",
			"secret_id_num_uses=0", "secret_id_ttl=0",
		)); err != nil {
			return fmt.Errorf("openbao %s: write approle %s: %w", id, svcRole, err)
		}
		svcRIDRes, err := client.Run(ctx, baoExec(rootToken, "read", "-format=json", "auth/approle/role/"+svcRole+"/role-id"))
		if err != nil {
			return fmt.Errorf("openbao %s: read role-id for %s: %w", id, svcRole, err)
		}
		var svcRID baoDataRoleID
		if err := json.Unmarshal([]byte(svcRIDRes.Stdout), &svcRID); err != nil || svcRID.Data.RoleID == "" {
			return fmt.Errorf("openbao %s: parse role-id for %s: %w", id, svcRole, err)
		}
		if _, err := f.resolver.EnsureString(ctx, "/mksrv/{env}/openbao/approle_"+id+"_svc_role_id", svcRID.Data.RoleID); err != nil {
			return fmt.Errorf("openbao %s: store role-id for %s: %w", id, svcRole, err)
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
		// dev role: any dev or admin (bare `bao login` lands here). admin role:
		// requested explicitly with `-role=tenant-<id>-admin`.
		if _, err := client.RunInput(ctx, baoExec(rootToken, "write", oidcGroupRolePath(id, "dev"), "-"),
			oidcGroupRolePayload([]string{"dev", "admin"}, role+"-dev")); err != nil {
			return fmt.Errorf("openbao %s: oidc dev role: %w", id, err)
		}
		if _, err := client.RunInput(ctx, baoExec(rootToken, "write", oidcGroupRolePath(id, "admin"), "-"),
			oidcGroupRolePayload([]string{"admin"}, role+"-admin")); err != nil {
			return fmt.Errorf("openbao %s: oidc admin role: %w", id, err)
		}

		// Mirror the tenant's own connection secrets into its KV path so the
		// team can read them with their AppRole / OIDC token. SSM stays mksrv's
		// source of truth; the passwords are write-once, so a mirror is only
		// written when it is missing or has drifted.
		stacks := f.data.Tenants[id].Stacks
		env := f.data.Deployment.Env
		if slices.Contains(stacks, "database") {
			pw, err := f.resolver.Get(ctx, "/mksrv/{env}/database/tenant_"+id+"_password")
			if err != nil {
				return fmt.Errorf("openbao %s: database password: %w (provision databases first)", id, err)
			}
			if err := mirrorTenantSecret(ctx, client, rootToken, id, "database", pw,
				tenantDBSecretFields(f.postgres, env, id, pw)); err != nil {
				return err
			}
		}
		if slices.Contains(stacks, "cache") {
			pw, err := f.resolver.Get(ctx, "/mksrv/{env}/cache/redis_"+id+"_password")
			if err != nil {
				return fmt.Errorf("openbao %s: cache password: %w (provision redis first)", id, err)
			}
			cacheHost := ""
			for _, ht := range f.targets {
				if slices.Contains(ht.Host.Stacks, "cache") {
					cacheHost = meshHostFQDN(env, ht.Name)
				}
			}
			if err := mirrorTenantSecret(ctx, client, rootToken, id, "cache", pw,
				tenantCacheSecretFields(cacheHost, id, pw)); err != nil {
				return err
			}
		}

		printer.Success("tenant %s: openbao admin/dev/svc policies + approle + transit + oidc + secrets (realm %s)", id, realm)
	}
	return nil
}
