# ADR 0040: Dedicated per-app Postgres databases, and the `files` stack for real

- Status: Accepted
- Date: 2026-10-21
- Milestone: M34

## Context

`files` (Samba + Nextcloud) has been a catalog descriptor since early in the
project — `stack.yaml` validates, but `templates: []` means `mksrv deploy`
never renders anything. `analytics` (Superset) is in the same state. Both are
third-party, per-tenant applications that manage their own schema migrations
and internal permissions, unlike the tenant's own PostgREST-facing app, which
`tenantDatabaseSQL` (`internal/cli/database.go`) provisions against the
cluster-global RBAC buckets (ADR 0026) and the anon-by-default fix (ADR 0029).

Reusing that machinery for Nextcloud would mean fighting it: Nextcloud expects
to own its database outright (`CREATE TABLE`, arbitrary grants, its own
migration runner), not operate inside a schema wired to `mksrv_owner` /
`mksrv_app` / `mksrv_anon` and a `pgrst_pre_request()` function it has no use
for. The tenant's own `database:` block (`schema`, `extensions`,
`connection_limit`) is specifically about *that* tenant's PostgREST-facing
app — it was never meant to also host a second, unrelated application's data.

## Decision

Every per-tenant stack that bundles a third-party app needing its own
database gets a **dedicated Postgres database + login role on the same shared
cluster** (Patroni when present, ADR 0017; otherwise the `database` stack's
standalone instance) — not a schema inside `db_<id>`, not a separate bundled
Postgres container per app.

- **`dedicatedAppDatabaseSQL(role, db, password string)`**
  (`internal/cli/appdb.go`) is the one shared helper: `CREATE ROLE ... LOGIN`,
  `CREATE DATABASE ... OWNER`, revoke `PUBLIC`, grant the role everything on
  its own database. No RBAC buckets, no PostgREST wiring — the role owns its
  database the way its own upstream docs expect. Naming convention:
  `<app>_<tenant-id>` for both the role and the database (`nextcloud_mcps`).
  Every future per-app-database stack (`analytics`'s Superset, and the
  `marketing` stack planned next) reuses this same helper instead of
  re-deriving its own SQL.
- **Each stack gets its own `internal/cli/<stack>.go` reconciler**
  (`provisionFilesTenants` here), modeled directly on
  `reconcilePostgREST` (`internal/cli/postgrest.go`): find the host carrying
  the stack, loop over consuming tenants (anchored to the *full* tenant set,
  not just the current `--tenant` selection — the same
  `postgrestConsumerIDs` reasoning applies to any sorted-port-index scheme),
  render per-tenant Quadlet units with `render.One`, write/restart them by
  hand, and tear down (best-effort, idempotent) tenants that dropped the
  stack. This is **not** a new generic `stack.yaml` field — it mirrors how
  `cache`, `openbao`, and `mail` already provision tenant-specific resources
  as hand-written Go, not declarative config.
- **`files` is now fully implemented**: Nextcloud (`docker.io/library/
  nextcloud:30-apache`, auto-installs from `POSTGRES_*`/`NEXTCLOUD_ADMIN_*`
  env vars — no init hook needed, confirmed live against a throwaway Postgres)
  and Samba (`ghcr.io/servercontainers/samba:a3.24.2-s4.23.8-r0` — the
  previously-pinned `4.20.6` tag no longer exists upstream, confirmed via
  `skopeo list-tags`; this fixes a stack that would have failed to pull on
  its very first real deploy). Nextcloud gets a Caddy vhost on the edge
  (`<id>.files.<root_domain>`, `docs/reference/stacks.md`); Samba is
  tailnet/loopback-only (SMB has no reverse-proxy story). Both get consecutive
  per-tenant host ports (`filesNextcloudBasePort` 8030, `filesSambaBasePort`
  4450, ten tenants each, `internal/cli/files.go`), opened in the Headscale
  ACL (`internal/headscale/headscale.go`'s `fleetPorts`).

## Consequences

### Positive

- Nextcloud/Superset/Keila/Plunk/Umami each behave exactly as their own
  upstream documentation describes — no mksrv-specific permission model to
  work around.
- One shared SQL helper instead of five near-identical ad hoc
  `CREATE DATABASE` blocks across `files`/`analytics`/`marketing`.
- A tenant that drops the stack gets a clean, idempotent teardown — the
  database itself is deliberately left behind (not dropped), matching how
  `provisionDatabases`/`reconcilePostgREST` already treat tenant data as never
  auto-deleted.

### Negative

- A tenant now accumulates one Postgres database per third-party app it
  opts into, alongside `db_<id>`. Not a real scaling concern at this fleet's
  size, but worth remembering if a tenant opts into several of these stacks.
- Samba's fixed, non-standard per-tenant port (4450+) means Windows' built-in
  SMB client — which cannot target a non-default port — won't work out of the
  box; documented in `docs/how-to/files.md` rather than solved (a per-tenant
  virtual mesh identity for Samba is out of scope here).

## Alternatives discarded

- **A schema inside `db_<id>`**, reusing `tenantDatabaseSQL`. Rejected: forces
  every third-party app's migrations to coexist with the tenant's own RBAC
  buckets and `pgrst_pre_request()`, neither of which the app understands or
  needs.
- **A generic `stack.yaml` field** (e.g. `apps[].needs_database: true`) driving
  a fully declarative reconciler. Would generalize the mechanism once, but
  every other per-tenant resource in this codebase (Redis ACLs, OpenBao
  policies, mail accounts) is already hand-written Go, not declarative YAML —
  a generic field here would be the one stack-level knob that works
  differently from everything else next to it.
- **A separate bundled Postgres container per app** (as `identity` does for
  Keycloak). Rejected for new stacks: duplicates what the shared cluster
  already provides, and ADR 0017 already moved `database` itself onto the
  cluster for exactly this reason.
