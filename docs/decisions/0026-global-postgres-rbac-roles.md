# ADR 0026: Global Postgres RBAC roles

- Status: Accepted
- Date: 2026-09-07
- Milestone: M26
- Supersedes: the Postgres part of ADR 0016 (M19)

## Context

ADR 0017 puts every tenant database in one Patroni cluster. PostgreSQL role
existence and membership are **cluster-global** (`pg_authid` / `pg_auth_members`
are shared catalogs); only what a role may do to schemas and tables is
per-database (`pg_namespace.nspacl`, `pg_class.relacl`, `pg_default_acl`).

M19 (ADR 0016) creates five roles per tenant — `<id>` (dev/admin, owns the `app`
schema), `<id>_app`, `<id>_anon`, `<id>_web`, `<id>_auth` — and one
`app.pgrst_pre_request()` function per tenant database that `SET LOCAL ROLE`s
between them. Every role but the two that log in does exactly the same job for
every tenant; the difference between `hg_app` and `bitabit_app` is nothing.

The cost of that:

- `pg_roles` lists all `5N` roles to anyone connected to any database, so a
  tenant opening `\du` in a SQL client reads the names (and RBAC wiring) of
  every other tenant — cosmetically alarming even though it grants no access.
- N copies of an identical `pgrst_pre_request()` definition.
- A per-tenant `role: <id>_web` hardcoded claim mapper in each realm.
- Onboarding a tenant creates five roles instead of two.

Nothing is deployed against real data yet, so this is a clean redesign, not a
migration.

## Decision

### Four global privilege buckets, created once

`NOLOGIN`, no role attributes, created by `mksrv postgres bootstrap` (and
idempotently re-asserted by `mksrv tenant apply`) before any tenant:

| role | job |
|---|---|
| `mksrv_owner` | owns the `app` schema and its objects in **every** tenant database; the DDL identity for `admin`/`dev` |
| `mksrv_app` | `apps` group — `SELECT` on `app` by default, dev opens writes per table |
| `mksrv_anon` | token-less requests |
| `mksrv_web` | `NOINHERIT`; the role PostgREST impersonates from the JWT `role` claim, member of `mksrv_owner` / `mksrv_app` / `mksrv_anon` |

`GRANT mksrv_owner, mksrv_app, mksrv_anon TO mksrv_web;` — one membership graph,
identical for the whole fleet.

### Two login roles per tenant

These keep a password and the connect grant, which are the isolation boundary —
a shared login role granted `CONNECT` on every `db_<id>` would let anyone
holding one tenant's credential reach all of them.

| role | job |
|---|---|
| `<id>_login` | humans (`admin` / `dev`) over the VPN with `psql` / a SQL client; `LOGIN`, password in SSM `tenant_<id>_password`, member of `mksrv_owner` |
| `<id>_auth` | the PostgREST authenticator; `LOGIN NOINHERIT`, password in SSM `tenant_<id>_authpw`, member of `mksrv_web` |

```sql
CREATE DATABASE db_<id> OWNER <id>_login;         -- DB lifecycle stays tenant-scoped
REVOKE ALL ON DATABASE db_<id> FROM PUBLIC;
GRANT CONNECT, CREATE ON DATABASE db_<id> TO <id>_login;
GRANT CONNECT ON DATABASE db_<id> TO <id>_auth;
ALTER ROLE <id>_login SET role TO mksrv_owner;    -- see below
```

The database is owned by `<id>_login`, **not** `mksrv_owner`: a global database
owner could `ALTER DATABASE db_<other>` from any session (a cross-tenant DoS
vector). `mksrv_owner` owns nothing above schema level.

### `ALTER ROLE <id>_login SET role TO mksrv_owner`

An object is owned by whoever runs `CREATE`, not by an inherited role. Without
this, a table a dev creates in a direct `psql` session is owned by `<id>_login`,
so `ALTER DEFAULT PRIVILEGES FOR ROLE mksrv_owner` does not cover it and
`mksrv_app` / `mksrv_anon` silently lose `SELECT` on new tables.

`ALTER ROLE … SET role` makes every `<id>_login` session start as `mksrv_owner`,
so DDL — direct or via PostgREST's `SET LOCAL ROLE mksrv_owner` — always produces
`mksrv_owner`-owned objects and one set of default privileges applies. Logs and
`pg_stat_activity` still key on `session_user` (`<id>_login`), so per-tenant
attribution is intact; only `current_user` reads `mksrv_owner`. A human can
`RESET role` to step back to `<id>_login`, which holds strictly fewer privileges.

### Constant pre-request function and claim

`app.pgrst_pre_request()` becomes one definition (a `const` in
`internal/cli/database.go`), referencing `mksrv_owner` / `mksrv_app` /
`mksrv_anon`. The Keycloak hardcoded mapper becomes `role: mksrv_web` for every
realm.

### Per-database grants unchanged in shape

`GRANT USAGE ON SCHEMA app`, `ALTER DEFAULT PRIVILEGES … IN SCHEMA app`,
`GRANT SELECT ON ALL TABLES IN SCHEMA app` still run inside each `db_<id>` during
`tenant apply`; only the grantee names change from `<id>_*` to `mksrv_*`. Object
privileges stay per-database, so a wrong grant is still contained to one tenant.

### No migration

Nothing has run against these databases. Rollout is: drop `db_{bitabit,hg,mcps}`
and the old `<id>` / `<id>_{app,web,anon}` roles on the Patroni primary, then
`mksrv tenant apply` rebuilds everything in the new shape (and re-writes the
Keycloak mapper, so fresh tokens carry `role: mksrv_web`). PostgREST containers
redeploy for the `PGRST_DB_ANON_ROLE=mksrv_anon` change.

## Consequences

- Roles drop from `5N` to `4 + 2N` (N=3: 15 → 10; N=40: 200 → 84). `pg_roles`
  shows generic bucket names plus `<id>_login` / `<id>_auth` per tenant.
- **Blast radius up**: a role *attribute* change (`SUPERUSER`, `BYPASSRLS`,
  `CONNECTION LIMIT`, `LOGIN`) on a bucket is fleet-wide. mksrv sets no
  attributes on the buckets, and `CONNECTION LIMIT` stays on the per-tenant
  `<id>_auth`. Object grants remain per-database.
- **Tenant data isolation is unchanged** — still gated by `GRANT CONNECT ON
  db_<id>` to the two per-tenant login roles only. `SET ROLE` cannot cross
  databases; `mksrv_owner` owns no database.
- **Observability**: `pg_stat_activity.usename` is `mksrv_web` / `<id>_auth` /
  `<id>_login`; attribute activity by `datname` (how `postgres_exporter` and the
  dashboards already group). `session_user` and `log_line_prefix %u` still name
  the tenant.
- `SELECT current_user` returns `mksrv_owner` in a human session — documented in
  `docs/rbac.md`.
- `<id>_login` and `<id>_auth` still name the tenant in `pg_roles` (and
  `db_<id>` in `pg_database`). Hiding those needs role/db-name obfuscation
  (consistent across realm names, `*.rest` DNS certs, Headscale users — a
  separate, larger change) or one cluster per tenant. Explicitly out of scope.
- Touchpoints: `internal/cli/database.go` (`ensureGlobalDBRoles`, slimmed
  `tenantDatabaseSQL`, constant pre-request SQL), `internal/cli/postgres.go`
  (call `ensureGlobalDBRoles` in `bootstrap`), `internal/cli/postgrest.go`
  (`PGRST_DB_ANON_ROLE`), `internal/cli/tenant.go` (the `role` claim),
  `internal/cli/openbao_tenant.go` (mirrored `username` → `<id>_login`),
  `docs/rbac.md`, ADR 0016 (superseded note).
- OpenBao per-group policies (M18), the Keycloak groups, and configd are
  untouched — only the Postgres realisation of the RBAC model changes.
