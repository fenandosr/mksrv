# ADR 0039: Tenant-declared database roles, configurable PostgREST, and declarative Keycloak clients

- Status: Accepted (§1, §2, §3, §6 implemented; §4-§5 pending)
- Date: 2026-11-12
- Refines: ADR 0016 (tenant Keycloak RBAC), ADR 0026 (global Postgres RBAC roles), ADR 0029 (tenant `database:` block)

## Context

Publishing a multi-profile line-of-business app (HG-LIMS, a clinical-lab
system: React SPA, PostgREST with row-level security, a Python worker) in the
`hg` tenant exposes four generic gaps. Any client with several user profiles
will hit them.

Verified against the code:

| Topic | Today |
|---|---|
| Roles | `globalRBACRolesSQL` creates `mksrv_owner/app/anon/web` on every `tenant apply`. Per tenant only `<id>_login` and `<id>_auth` exist. A tenant cannot declare more roles. |
| Pre-request | `pgrstPreRequestSQL(schema)` writes `<schema>.pgrst_pre_request()` with a fixed mapping: `admin`/`dev` → `mksrv_owner`, `apps` → `mksrv_app`, anything else → `mksrv_anon`. It is overwritten by `CREATE OR REPLACE` on every apply, so a tenant cannot extend it, and it appears as an RPC and in the OpenAPI document of the exposed schema. |
| Schemas | `PGRST_DB_SCHEMAS` is the single `DBSchema`. |
| JWT | The realm JWKS is fetched once per `tenant apply` (`fetchRealmJWKS`) and stored as a podman secret. A Keycloak key rotation breaks validation until the next apply. No `PGRST_JWT_AUD`. |
| Config | No CORS, `max_rows` or `app.settings.*`. |
| Ports | `postgrestBasePort` (3010) + alphabetical index; the ACL opens 3010-3019. A tenant sorting earlier shifts the others (already seen live with `gtex`). |
| Keycloak | `ClientSpec` supports only public/redirects/origins/hardcoded claims/groups claim. `tenantGroups` is the constant `admin/dev/apps/vpn`. Mappers are create-if-missing, so changing a value needs manual deletion (noted in ADR 0016). |

The goal is flexibility for this kind of client without weakening tenant
isolation or changing behaviour for existing tenants.

## Decision

Everything below is **optional**. A tenant that declares none of it produces
byte-identical SQL, container units and realm configuration (enforced by golden
tests on `bitabit`, `mcps`, `gtex`).

### 1. `database.postgrest` becomes an object (the boolean still works)

```yaml
database:
  postgrest:
    enabled: true
    schemas: [api]                 # first = default profile; default [schema]
    pre_request_hook: private.check_request
    jwt_audience: hg-lims-api
    cors_origins: [https://lims.example.org]
    max_rows: 1000
```

- Maps to `PGRST_DB_SCHEMAS`, `PGRST_JWT_AUD`,
  `PGRST_SERVER_CORS_ALLOWED_ORIGINS`, `PGRST_DB_MAX_ROWS`. Variables are
  emitted only when declared.
- Listed schemas are created if missing, owned by `mksrv_owner`.
- `PGRST_APP_SETTINGS_OIDC_ISSUER` is injected only when the object form is
  used (so existing containers do not change).
- One PostgREST instance per tenant stays. Several instances per tenant
  (`postgrest.instances[]`) are deferred; several projects = several schemas.

### 2. Pre-request generated from a declared mapping, plus a tenant hook

```yaml
database:
  roles:
    hg_reception: {member_of: [hg_authenticated], when: [reception]}
    hg_reception_finance: {member_of: [hg_reception, hg_finance], when: [reception, finance]}
  rbac:
    unmapped: deny                 # deny | anon
```

- `when` is a list of Keycloak groups that must **all** be present (AND).
  The first match wins. Rules are ordered deterministically, **most specific
  first** (more required groups, then role name): the YAML loader goes through
  JSON, so map declaration order is not preserved and cannot be the rule. No
  explicit priorities.
- If at least one `when` is declared, the `admin`/`dev` → `mksrv_owner`
  shortcut **is removed** for PostgREST unless the tenant declares it
  (`when: [admin]` → `mksrv_owner`). `unmapped` defaults to `deny`
  (HTTP 403). Without any `when`, behaviour is unchanged. `validate` emits the
  `rbac_shadow` warning.
- After `SET LOCAL ROLE`, `pre_request_hook` (if set) is invoked with
  `PERFORM`, already under the business role. It can raise to reject.
  Resolution is at run time, so the hook may be created by a later migration.
  The name must be `schema.function`, escaped with `%I`; reserved schemas are
  rejected.
- Token-less requests return early as today; the hook is not called.
- The function moves to a non-exposed schema (`mksrv.pgrst_pre_request`) **only
  for tenants that opt in** (any of `roles`, `rbac`, `pre_request_hook`).
  Existing tenants keep `<schema>.pgrst_pre_request` until a separate, explicit
  migration.

### 3. Tenant-declared roles

```yaml
database:
  role_prefix: hg                  # default: the tenant id
  default_privileges: none         # none | mksrv (default: mksrv, today's behaviour)
  roles:                           # NOLOGIN privilege buckets
    hg_authenticated: {}
  service_roles:                   # LOGIN; password in SSM, mirrored to OpenBao
    hg_worker: {member_of: [hg_worker_base], connection_limit: 5}
```

- `tenant apply` creates roles idempotently, applies declared membership, and
  grants every declared role to **`<id>_auth`**, not to `mksrv_web`. An
  authenticator can therefore only assume its own tenant's roles; there is no
  cross-tenant membership. `SET LOCAL ROLE` checks membership of the session
  user, which is how `mksrv_owner` already works through `mksrv_web`.
- Names must start with `<role_prefix>_`; `<id>_login`, `<id>_auth` and
  `mksrv_*` are reserved. Never `SUPERUSER`, `CREATEROLE`, `BYPASSRLS` or
  `REPLICATION`. Service roles get `CONNECT` only on `db_<id>`; passwords come
  from `EnsureRandom` like the others.
- Service-role passwords are generated into SSM (`tenant_<id>_svc_<role>_password`);
  mirroring them into OpenBao is not implemented yet. Membership is additive:
  removing a `member_of` entry from the YAML does not revoke it.
- mksrv manages existence, membership and `CONNECT` only. Object privileges
  belong to the app's migrations inside `db_<id>`.
- `default_privileges: none` stops the default grants to `mksrv_app` on the
  exposed schema; required for deny-by-default data (clinical).
- **Shared prefix across tenants** (e.g. `hg` and a staging tenant): roles are
  cluster-global, so both may declare the same prefix only if each shared
  role's definition (membership, attributes) is identical. This is checked by
  `validate` (`role_conflict`), not at apply time; the role is created once.
  Grants stay per database, so nothing leaks. Tenants that prefer strict
  isolation use distinct prefixes and parameterised migrations.

### 4. JWKS refresh

A systemd timer on the data host re-fetches each active realm's JWKS, compares,
and replaces it only on change. Preferred: mount the JWKS as a file and reload
with `SIGUSR2`. Fallback (simpler, robust): `podman secret create --replace`
plus a container restart only when the document changed. The mechanism is chosen
after verifying `SIGUSR2` reload against the pinned image (template default
v12.2.3; HG-LIMS uses v14), and the default image is bumped if needed.

### 5. Declarative Keycloak groups, clients and settings

```yaml
keycloak:
  groups: [reception, lab, finance]      # additive to admin/dev/apps/vpn
  settings: {access_token_lifespan: 300, refresh_token_rotation: true}
  clients:
    - id: hg-lims-web                    # must start with "<tenant>-"
      public: true
      redirect_uris: [https://lims.example.org/auth/callback]
      post_logout_redirect_uris: [https://lims.example.org/]
      pkce: S256
      direct_grants: false
      audience: [hg-lims-api]
      claims: {groups: true, postgrest_role: true}
```

- Validation: client id carries the tenant prefix; redirects are `https`,
  inside `base_domain`, no wildcards. `localhost` is allowed only in a tenant
  explicitly marked `environment: non-production`, never by default.
  `settings` is an allow-list with ranges.
- `mksrv-`-prefixed mappers reconcile **to desired state** (create, update,
  delete stale ones); foreign mappers are never touched. This closes the ADR
  0016 limitation.
- `tenantGroups` becomes `base ∪ keycloak.groups`; `*.users.yaml` validates
  against the union.
- Tenant admins still cannot edit clients or mappers (ADR 0016 boundary
  intact): changes go through a reviewed PR.
- **Out of scope for now:** a service account with `view-users`. It is a
  privilege escalation in Keycloak and gets its own ADR once a client confirms
  the need.

### 6. Stable PostgREST ports (prerequisite for a second tenant)

Persist each tenant's port in `mksrv.lock` (`postgrest_ports`) instead of
deriving it from the alphabetical index, and widen the Headscale ACL range to
3010-3099. A workspace with no lock entry is seeded with the old alphabetical
allocation, so live containers keep their ports; new tenants take the lowest
free port; ports of tenants that stop being consumers are freed. Must
land before adding a staging tenant.

### Deferred

- Tenant-owned PostgREST hostname inside `base_domain` (`postgrest.hostname`,
  reusing `web:` edge logic).
- Multiple PostgREST instances per tenant.

## Validation codes

| Code | Severity | When |
|---|---|---|
| `tenant.database.role_name` | error | name lacks the role prefix or is reserved |
| `tenant.database.role_member` | error | `member_of` references an undeclared role, or a cycle |
| `tenant.database.role_conflict` | error | two tenants share a prefix with different definitions |
| `tenant.database.when_group` | error | `when` uses a group absent from the realm |
| `tenant.database.hook_name` | error | hook is not `schema.function` or targets a reserved schema |
| `tenant.database.rbac_shadow` | warning | `when` declared: `admin`/`dev` → owner shortcut disabled |
| `tenant.keycloak.client_id` | error | id without the tenant prefix |
| `tenant.keycloak.redirect` | error | redirect outside `base_domain`, wildcard, or non-`https` |
| `tenant.keycloak.setting` | error | setting outside the allow-list or range |

The JSON Schema (`additionalProperties: false`) catches misspelled keys before
the semantic pass.

## Delivery order

1. §1 + §2 (unblock HG-LIMS), opt-in.
2. §6 stable ports.
3. §3 roles (enables a staging tenant).
4. §5 Keycloak.
5. §4 JWKS refresh.

## Consequences

- Existing tenants are unchanged (golden tests); no data migration.
- Data-plane authorisation for opted-in tenants moves from a fixed shortcut to
  an explicit group→role mapping. Note that "a developer token cannot read
  business data through the API" only holds when §2 is active, since
  `<id>_login` still maps to `mksrv_owner` for humans over the VPN.
- Cluster-global roles with a shared prefix create deliberate, validated
  coupling between tenants; strict per-tenant prefixes remain available.
- More surface to test: golden SQL, schema/semantic codes, Keycloak
  reconciliation, container template variables, and a throwaway-Postgres
  integration test (roles, `<id>_auth` membership, `SET LOCAL ROLE`, rejecting
  hook), then an end-to-end run on a synthetic staging tenant before touching
  `hg`.

## Open points

- Confirm Postgres version of `mksrv-postgres` and that `SET LOCAL ROLE` with
  `<id>_auth` as `session_user` behaves as described on it.
- Confirm `SIGUSR2` JWKS reload on the pinned PostgREST version.
- Confirm which Keycloak 26 admin endpoints cover the proposed `settings` keys.
- Whether to later migrate every tenant's pre-request function to the `mksrv`
  schema.
