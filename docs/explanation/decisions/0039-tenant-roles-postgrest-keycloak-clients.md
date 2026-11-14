# ADR 0039: Tenant-declared database roles, configurable PostgREST, and declarative Keycloak clients

- Status: Accepted, implemented (§1-§6); live verification pending (see Open points)
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

The role, group, client and setting features below are **optional**: a tenant
that declares none of them gets the same realm and base roles as before.

**Breaking change.** PostgREST itself is now **opt-in**, and the legacy
`<schema>.pgrst_pre_request` path is gone. When this landed, no application was
actually calling PostgREST, so no compatibility shim was kept, but the change is
not invisible: `hg` and `bitabit` had to add `database.postgrest: true`, and
`mcps` (which does not need a data API) was deliberately switched off.

- A tenant without `database.postgrest: true` (or the object form) gets no
  container, no `<id>_auth` role, no `mksrv.pgrst_pre_request` and no
  `<id>.rest.<root_domain>` DNS record. `tenant apply` removes anything left
  over from before: container, podman secrets, Caddy fragment, the `<id>_auth`
  role and its CONNECT grant, and the pre-request function. The only residue is
  the unused `_authpw` SSM parameter.
- **Every** PostgREST tenant, opted into the object form or not, now uses the
  generated pre-request in the non-exposed `mksrv` schema (§2), and the legacy
  `<schema>.pgrst_pre_request` is dropped from its exposed schema. Only the
  `roles` / `when` mapping, `rbac`, the hook and the exposure settings remain
  per-tenant opt-in.
- A pre-ADR function in a schema the tenant no longer names (for example `hg`,
  whose `schema` was changed after provisioning) is not found by that `DROP`
  and must be removed by hand.

### 1. `database.postgrest` becomes an opt-in object (`true` still works)

```yaml
database:
  postgrest:
    enabled: true
    schemas: [api]                 # first = default profile; default [schema]
    pre_request_hook: private.check_request
    jwt_audience: hg-lims-api
    cors_origins: [https://lims.example.org]
    max_rows: 1000
    openapi_mode: disabled         # follow-privileges (default) | ignore-privileges | disabled
    app_settings:                  # non-secret, plain text -> current_setting('app.settings.<key>', true)
    #  oidc_issuer: https://auth.example.com/realms/hg   # default: the tenant realm's issuer
    #  environment: staging
```

- Maps to `PGRST_DB_SCHEMAS`, `PGRST_JWT_AUD`,
  `PGRST_SERVER_CORS_ALLOWED_ORIGINS`, `PGRST_DB_MAX_ROWS`, `PGRST_OPENAPI_MODE`.
  Variables are emitted only when declared. PostgREST's default serves the
  OpenAPI document (schema and server version) to anonymous callers on `GET /`;
  clinical tenants should set `openapi_mode: disabled`.
- Listed schemas are created if missing, owned by `mksrv_owner`.
- `PGRST_APP_SETTINGS_OIDC_ISSUER` is always injected (from the tenant realm), so
  `current_setting('app.settings.oidc_issuer', true)` works out of the box and
  cannot drift from the realm. `app_settings` adds more keys, or overrides
  `oidc_issuer`: each key `k` becomes `PGRST_APP_SETTINGS_<K>` and is readable as
  `app.settings.<k>`. Keys are `[a-z0-9_]{1,63}`, values plain text. They are
  written **in clear text** to the container unit on the data host, so only
  non-secret values are allowed: `validate` (`tenant.database.app_setting`)
  rejects references to OpenBao/SSM or interpolation, and characters unsafe in
  a systemd `Environment=` line. PostgREST itself checks only signature and
  `aud`; validating `iss` inside the database is a defence in depth the tenant
  hook can build on this setting. Changing the map restarts the tenant's
  PostgREST on the next `tenant apply`, as any unit change does (every
  `tenant apply` currently restarts the selected PostgREST containers).
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
  The first match wins. Rules are ordered deterministically: a role that
  **inherits** another (`member_of`, transitively) before the roles it
  includes, then more required groups, then role name. Inheritance first means a
  user holding the groups of both `lab` and `reporting` lands in
  `hgstg_reporting` (which includes `hgstg_lab`) and not in the narrower role;
  two unrelated single-group roles a user holds at once (say `finance` and `hr`)
  are broken by name, so declare a combined role with `when: [finance, hr]` when
  that combination matters. The YAML loader goes through JSON, so map
  declaration order is not preserved and cannot be the rule. No explicit
  priorities.
- If at least one `when` is declared, the `admin`/`dev` → `mksrv_owner`
  shortcut **is removed** for PostgREST unless the tenant declares it
  (`when: [admin]` → `mksrv_owner`). `unmapped` defaults to `deny`
  (HTTP 403). Without any `when`, behaviour is unchanged. `validate` emits the
  `rbac_shadow` warning.
- After `SET LOCAL ROLE`, `pre_request_hook` (if set) is invoked with
  `PERFORM`, already under the business role: that role needs `USAGE` on the
  hook's schema and `EXECUTE` on the function (mksrv grants neither; the app's
  migrations do). It can raise to reject.
  Resolution is at run time, so the hook may be created by a later migration.
  The name must be `schema.function`, escaped with `%I`; reserved schemas are
  rejected.
- Token-less requests return early as today; the hook is not called.
- The function lives in a non-exposed schema (`mksrv.pgrst_pre_request`) for
  every PostgREST tenant (see the breaking-change notice).

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
  `REPLICATION`. Service roles are granted `CONNECT` on `db_<id>` only, but `PUBLIC` still has the
  default `CONNECT` on the shared databases (`postgres`, `app`) and on any
  other database whose ACL was never restricted, so a service role (like
  `<id>_login` today) can open a session there; it has no privileges inside.
  Revoking `CONNECT` from `PUBLIC` on those databases is a follow-up (the
  postgres exporter uses the superuser, which is unaffected; Patroni and
  pgAdmin must be checked first); passwords come
  from `EnsureRandom` like the others.
- Service-role passwords are generated into SSM (`tenant_<id>_svc_<role>_password`)
  and, for tenants with the `openbao` stack, mirrored to
  `kv/tenants/<id>/database-<role>` (same write-if-missing-or-drifted rule as the
  tenant's own database secret). The tenant `admin` policy has full control of
  `kv/tenants/<id>/*` (intentional, same as for `database`), so an admin can
  overwrite the entry: the next `tenant apply` restores it only when the
  stored `password` differs from SSM, and SSM (not OpenBao) is the source of
  truth for what Postgres actually has. Membership among the tenant's
  declared roles is declarative: a membership between two of them that the YAML
  no longer lists (for example a service role that must not inherit the API
  roles) is revoked on the next `tenant apply`. Memberships involving roles
  outside the declared set, and `<id>_auth`'s grants, are not touched.
- mksrv manages existence, membership and `CONNECT` only. Object privileges
  belong to the app's migrations inside `db_<id>`.
- `schemas: [app, private, legacy]` creates extra schemas that PostgREST does
  **not** expose, owned by `mksrv_owner` (the migration identity runs as
  `mksrv_owner` and has no `CREATE` on the database, so it cannot create them
  itself). They get no `USAGE` for `mksrv_app` / `mksrv_anon` / `mksrv_web`: the
  app's migrations grant what its roles need. `validate` rejects reserved
  names and any schema that is the tenant's main or PostgREST-exposed one.
  Because `<id>_login` sessions run as `mksrv_owner`, a migration tool can keep
  its bookkeeping table in such a schema (`private.schema_migrations`).
- `default_privileges: none` stops the default grants to `mksrv_app` on the
  exposed schema; required for deny-by-default data (clinical).
- **Shared prefix across tenants** (e.g. `hg` and a staging tenant): roles are
  cluster-global, so both may declare the same prefix only if each shared
  role's definition (membership, attributes) is identical. This is checked by
  `validate` (`role_conflict`), not at apply time; the role is created once.
  Grants stay per database, so nothing leaks. Tenants that prefer strict
  isolation use distinct prefixes and parameterised migrations.

### 4. JWKS refresh

A systemd timer (hourly, `mksrv-postgrest-jwks-refresh.timer`) on the data host
re-fetches each active realm's JWKS, compares, and replaces it only on change.
Implemented with the fallback below; the `SIGUSR2` variant is not. Preferred: mount the JWKS as a file and reload
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
  inside `base_domain`, no wildcards. `localhost` is allowed only with the
  explicit `keycloak.allow_localhost: true` on the tenant (non-production), never
  by default. `settings` is an allow-list whose ranges are enforced by the JSON
  Schema (so a violation reports as a schema issue, not
  `tenant.keycloak.setting`). Ranges: `access_token_lifespan` 60-3600 s,
  `sso_session_idle` 300-86400 s.
- `mksrv-`-prefixed mappers reconcile **to desired state** (create, update
  drifted values in place on every client; delete stale ones only on
  tenant-declared clients, since other code paths add their own `mksrv-*`
  mappers such as `mksrv-audience`); foreign mappers are never touched. This closes the ADR
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
free port; ports of tenants that stop being consumers are freed.

**Operational note:** the seed is computed from the tenants present at that
moment. Adding a tenant that sorts alphabetically *between* live ones, in the
same change that first introduces `postgrest_ports`, would move the later
tenants (found while preparing `hgstg`: it would have pushed `mcps` from 3012 to
3013). Pin the live ports in `mksrv.lock` (or run one apply with the new binary
before adding the tenant) first.

Terraform types `database.postgrest` as `object({enabled})`; the settings are
reduced to the enabled flag before reaching Terraform
(`normalizeTenantsForTerraform`), and only enabled tenants get a
`<id>.rest.<root_domain>` record.

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

- No data migration, but not unchanged: PostgREST is opt-in, the legacy
  pre-request path is gone (see the breaking-change notice), and tenants that
  keep PostgREST move to `mksrv.pgrst_pre_request`.
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

- Invariants worth stating, because the tests of this ADR found them:
  - **`jwt_audience` is the real barrier between clients of one realm.** Any
    other client in the realm that carries the `role` and `groups` mappers
    produces tokens PostgREST would otherwise accept; it is the `aud` check that
    rejects them. A PostgREST tenant should always set `jwt_audience`.
  - **`<id>_auth` is a transitive member of `mksrv_owner`** through
    `mksrv_web` (ADR 0026), by design: the generated pre-request is what keeps
    a request from landing there, so no `when` rule may target a role that
    reaches `mksrv_owner`.
  - Roles are cluster-global: with the default prefix (the tenant id) nothing is
    shared; `role_prefix` sharing between tenants (a staging tenant reusing
    `hg_*`) is validated but has not been exercised live.

## Open points

- PostgREST is pinned to v12.2.3 in the template; HG-LIMS validates against
  14.x. Bumping the image is a separate decision (it affects every tenant).
- `REVOKE CONNECT ... FROM PUBLIC` on `postgres` and `app` (see §3).

- Confirm Postgres version of `mksrv-postgres` and that `SET LOCAL ROLE` with
  `<id>_auth` as `session_user` behaves as described on it.
- Confirm `SIGUSR2` JWKS reload on the pinned PostgREST version.
- Confirm which Keycloak 26 admin endpoints cover the proposed `settings` keys.
- Whether to later migrate every tenant's pre-request function to the `mksrv`
  schema.
