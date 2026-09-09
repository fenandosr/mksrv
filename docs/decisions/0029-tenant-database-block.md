# ADR 0029: The tenant `database:` block — PostgREST opt-out, schema, extensions, and anon-by-default fix

- Status: Accepted
- Date: 2026-09-09
- Milestone: M29
- Refines: ADR 0019 (PostgREST), ADR 0026 (global Postgres RBAC)

## Context

A tenant that lists the `database` stack gets, with no further configuration:

- a database `db_<id>`, the `<id>_login` / `<id>_auth` roles, and an `app` schema
  wired to the global RBAC buckets (ADR 0026);
- a **PostgREST container** on the data host, fronted by the edge at the public
  FQDN `<id>.rest.<root_domain>` (Caddy vhost + operator-zone A record) and also
  reachable on the tailnet;
- `ALTER DEFAULT PRIVILEGES … GRANT SELECT ON TABLES TO mksrv_app, mksrv_anon`
  on the `app` schema — so every table a tenant creates is readable both by the
  authenticated `apps` group **and by `mksrv_anon`, the role PostgREST uses for
  token-less requests**.

Two problems surfaced when onboarding a tenant (Huella Génica) whose database
holds data regulated under NOM-024-SSA3-2012 and who has no intention of exposing
a public data API:

1. **`mksrv_anon` is granted `SELECT` by default, and the PostgREST URL is on
   the public internet.** `curl https://<id>.rest.<root_domain>/<table>` returns
   rows with no credential. The blanket default privilege means a tenant that
   runs `CREATE TABLE patients (…)` has published it, silently, the moment
   PostgREST reloads its schema cache. The framework's safe default is "world
   -readable"; it should be "nothing".

2. **PostgREST is not optional.** A tenant that only wants Postgres reachable
   over the VPN (Django talking to `db_<id>` directly, no auto-generated REST
   layer) still gets a public `*.rest` vhost and a container it never asked for —
   extra public surface, extra moving part, for zero benefit.

Alongside these, three smaller knobs had no home and were being hand-patched into
the provisioning SQL: the application **schema name** (some ORMs and some
compliance layouts want their own, not `app`), **contrib extensions** (`pgcrypto`,
`uuid-ossp`, …), and a **per-tenant connection cap** so one tenant cannot exhaust
the shared Patroni cluster's `max_connections`.

## Decision

### 1. `mksrv_anon` gets no privileges by default (all tenants)

`tenantDatabaseSQL` drops `mksrv_anon` from `ALTER DEFAULT PRIVILEGES` and from
`GRANT SELECT ON ALL TABLES`. `mksrv_anon` keeps only `USAGE` on the schema (it
needs that to resolve names for the tables it *is* later granted) and `EXECUTE`
on `pgrst_pre_request()`.

Anonymous read access is now **opt-in per object**: a `dev` runs
`GRANT SELECT ON <schema>.<table> TO mksrv_anon` (optionally with RLS) for
exactly what should be world-readable through the public URL. `mksrv_app` — the
authenticated, group-gated bucket — keeps the schema default, so the
`apps`-group experience is unchanged.

The provisioning SQL also **heals databases created before this ADR**:

```sql
ALTER DEFAULT PRIVILEGES FOR ROLE mksrv_owner IN SCHEMA <schema>
  REVOKE SELECT ON TABLES FROM mksrv_anon;
REVOKE SELECT ON ALL TABLES IN SCHEMA <schema> FROM mksrv_anon;
```

Idempotent; runs on every `mksrv tenant apply`. A tenant that was relying on the
old blanket grant re-adds the specific `GRANT`s it actually wants.

### 2. `database:` block in `tenants/<id>.yaml`

```yaml
stacks: [database, …]
database:
  postgrest: false            # default true
  schema: appdata             # default "app"
  extensions: [pgcrypto, uuid-ossp]
  connection_limit: 40        # <id>_login concurrent connections; default unlimited
```

- **`postgrest`** (bool, default `true`) — when `false`, `reconcilePostgREST`
  tears down the container, its `podman` secrets, and the edge Caddy fragment
  (`21-postgrest-<id>.caddy`), and `infra/root` drops `<id>.rest.<root_domain>`
  from the operator zone and the edge cert SAN list. The database, roles, schema,
  and `pgrst_pre_request()` function are still created — Postgres over the VPN is
  unchanged; only the public REST layer goes away.
- **`schema`** (string, default `"app"`) — the tenant's application schema and,
  when PostgREST runs, the only schema it exposes (`PGRST_DB_SCHEMAS`,
  `PGRST_DB_PRE_REQUEST`). Reserved names (`public`, `pg_catalog`,
  `information_schema`, `pg_toast`, anything `pg_*`) are rejected in validation.
- **`extensions`** (string[], allow-listed) — `CREATE EXTENSION IF NOT EXISTS`
  per entry in `db_<id>`. The allow-list is the twelve contrib extensions
  bundled with the Postgres image that add no untrusted procedural language and
  need no image change: `pgcrypto`, `uuid-ossp`, `citext`, `pg_trgm`,
  `btree_gist`, `btree_gin`, `hstore`, `unaccent`, `ltree`, `intarray`,
  `tablefunc`, `fuzzystrmatch`. Anything else is a validation error (the operator
  adds it to `allowedDBExtensions` if it belongs).
- **`connection_limit`** (int 1–500) — `ALTER ROLE <id>_login … CONNECTION LIMIT
  n`. Guards the shared Patroni cluster's `max_connections` (200, split across
  every tenant). `<id>_auth` (PostgREST, its own pool) is not capped. Default
  `-1` (unlimited).

The block is optional; omitting it keeps today's behaviour except for the
`mksrv_anon` change, which applies unconditionally.

### 3. Validation

`checkTenantDatabase` (semantic pass) emits:

| code | severity | when |
|---|---|---|
| `tenant.database.no_stack` | error | `database:` present but the tenant doesn't consume the `database` stack |
| `tenant.database.schema` | error | schema name is reserved / `pg_*` |
| `tenant.database.extension` | error | extension not in the allow-list |
| `tenant.database.schema_unused` | warning | `schema` set while `postgrest: false` (it still names the app schema, but PostgREST won't use it) |

JSON-schema `additionalProperties: false` catches typo'd keys before the
semantic pass runs.

## Consequences

- **Security posture flips to deny-by-default.** A fresh tenant table is private
  until a `dev` explicitly publishes it. This is a behaviour change for any
  existing tenant that relied on the blanket anon grant — the heal SQL will
  *remove* their public read access on the next `tenant apply`. For the live
  fleet (three tenants, none yet in production use) this is the intended fix, not
  a regression; the ADR is the migration note.
- **`postgrest: false` removes public surface.** One fewer container, one fewer
  Caddy vhost, one fewer cert SAN, one fewer A record per opted-out tenant. The
  edge's `*.rest` exposure now tracks actual intent.
- **`connection_limit` makes the shared cluster safer** but can also lock a
  tenant out if set too low; it's advisory, operator-set, and easy to raise.
- **The allow-list is deliberately conservative.** It excludes `postgis`,
  `plpython3u`, `pg_cron`, `timescaledb`, etc. — each needs an image change, a
  superuser-trust decision, or a background worker slot. Adding one is an
  operator PR touching `allowedDBExtensions`, not a tenant self-service knob.
- **Tooling touchpoints**: `schemas/tenant.v1.json` (`database` block),
  `internal/model` (`TenantDatabase`, `DBSchema` / `PostgRESTEnabled` /
  `DBExtensions` / `DBConnectionLimit`), `internal/workspace/semantic.go`
  (`checkTenantDatabase`, `allowedDBExtensions`, `reservedSchemas`),
  `internal/cli/database.go` (`tenantDatabaseSQL` schema/extension/limit +
  anon-revoke heal, `pgrstPreRequestSQL(schema)`),
  `internal/cli/postgrest.go` (`PostgRESTEnabled` gate + teardown loop for
  disabled tenants), `stacks/database/templates/postgrest.container.tmpl`
  (`PGRST_DB_SCHEMAS` / `PGRST_DB_PRE_REQUEST` from `DBSchema`),
  `infra/root/main.tf` (`tenant_rest_fqdns` filtered by `database.postgrest`).
- **Docs**: `docs/rbac.md` updated (anon no longer has a default `SELECT`). The
  workspace `docs/tenant-dev-guide.md` states "Django's own tables … land there
  too — harmless"; that line predates this ADR and is wrong for a
  security-sensitive tenant (those tables would have been anon-readable under the
  old default). It needs correcting in the workspace repo to reflect
  deny-by-default and to recommend a dedicated schema or `postgrest: false`.
- Deferred: a CDN/WAF in front of `*.rest` (ADR 0028 territory); per-table
  publish helpers (`mksrv tenant ... expose <table>`); moving `max_connections`
  itself under mksrv control so `connection_limit` can be validated against it.
