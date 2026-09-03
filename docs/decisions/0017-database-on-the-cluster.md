# ADR 0017: `database` runs on the Patroni cluster

- Status: Accepted
- Date: 2026-09-03
- Milestone: M20

## Context

The `postgres` stack (ADR 0013) has been a HA Patroni cluster since M9, but no
consumer used it — `database` shipped its own standalone `postgres:16` container
and `provisionDatabases` / PostgREST talked to that. Running both in the
distributed profile means two Postgres instances on the fleet.

## Decision

`database` uses the Patroni cluster when a `postgres` cluster is in the fleet,
and keeps its standalone Postgres otherwise (single-host / `targets: local` dev).

- **Auto-detected**, no config flag: `f.postgres.Primary != ""` (i.e.
  `.mksrv/postgres.json` exists from `mksrv postgres bootstrap`) selects cluster
  mode.
- `stacks/database/templates/postgres.container.tmpl` is wrapped in
  `{{- if not (.StackIP "postgres") }} … {{- end }}`; a template that renders to
  whitespace is now dropped by `deploy.dropEmpty` (`internal/deploy/stack.go`),
  so no standalone unit is written when a cluster exists.
- `provisionDatabases` runs `tenantDatabaseSQL` (unchanged) against the Patroni
  primary (`mksrv-patroni`, superuser `postgres`, password
  `/mksrv/<env>/postgres/superpass`). Patroni's `pg_hba` already allows
  `host all all 10.20.0.0/16 scram-sha-256`.
- PostgREST connects with a libpq multi-host DSN —
  `postgres://<id>_auth:<pw>@ip1:5432,ip2:5432,ip3:5432/db_<id>?target_session_attrs=read-write`
  — so it always reaches the writable primary and survives a failover
  (`postgrestDSN`, PostgREST v12).
- pgAdmin registers the primary's private IP instead of the `mksrv-postgres`
  container name.
- If a host carries `postgres` but the cluster is not bootstrapped,
  `provisionDatabases` errors with "run `mksrv postgres bootstrap` first".

### Keycloak's Postgres — deferred

`identity` keeps its dedicated `mksrv-identity-postgres`. It is small, on the
critical path, and moving it (schema migration, failover semantics for the
Keycloak connection pool) is a separate, riskier change. ADR 0004's
single-Keycloak decision is unaffected.

## Consequences

- `stacks/postgres` `pgdata` volume drops from 40 GiB @ 4000 IOPS to 20 GiB
  baseline (3000 IOPS, no surcharge) — three small tenant DBs don't need the
  provisioned IOPS.
- `database` `min_ram_mb` 1024 → 768 (pgAdmin + PostgREST only in cluster mode).
- The `/mksrv/<env>/database/pg_superpass` SSM param is unused in cluster mode
  (the cluster's `superpass` is the source of truth).
- Deferred: `provisionDatabases` targeting a specific primary vs. the multi-host
  DSN for the SQL too; folding `postgres bootstrap` into `apply`.
