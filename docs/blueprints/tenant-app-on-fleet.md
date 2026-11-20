# Blueprint: a tenant app on the fleet

How a tenant's developers put their own application into production on mksrv
services, taking responsibility for its availability, with no separate staging
environment. Status: **draft** (see the [README](README.md) for the markers).

## 1. Who does what

| Actor | Owns | Never touches |
|---|---|---|
| **Tenant developer** | The application code, its migrations, its deployment and its uptime | The fleet, or any secret that is not the tenant's |
| **Operator (mksrv)** | The fleet's services (Postgres, Redis, S3, Keycloak, OpenBao, DNS/TLS at the edge) and the tenant's identity in them | The tenant's code |
| **Deploy agent** | The boundary between the two: receives a package, validates it, builds, activates, rolls back | Anything outside its closed list of verbs |

**The property that makes this safe:** the developer never holds a privileged
shell or a fleet credential. They hold a socket with a closed set of verbs, and
OpenBao credentials that are read-only, narrow, and bound to the node's address.

**Verified (HG-LIMS):** the agent is a dispatcher running as an unprivileged
per-tenant service user. A systemd socket starts one instance per connection; it
reads **one line**, validates it against a closed list of verbs and arguments,
and executes without a shell. Code arrives as a package whose reference is the
SHA-256 of its contents (so the reference *is* the content). Changes run as
`systemd-run --user` jobs, so a dropped SSH session cannot interrupt them. See
[ADR 0034](../explanation/decisions/0034-tenant-local-hosts.md) and the draft
[ADR 0036](../explanation/decisions/0036-per-tenant-service-user-local-hosts.md).

## 2. What the fleet gives the app

| The app needs | The fleet gives | Declared as | The credential lands in |
|---|---|---|---|
| A SQL database | A database `db_<id>` on the Patroni cluster, with a login role and optional service roles | `database:` (`postgrest: false` for an ORM talking straight to Postgres) | `kv/tenants/<id>/database`, `database-<role>` |
| An object store | An S3 bucket, a dedicated IAM user and its key | `storage.s3` ([ADR 0041](../explanation/decisions/0041-tenant-storage-s3-and-approles.md)) | `kv/tenants/<id>/storage-s3` |
| Single sign-on | A Keycloak realm per tenant and declarative OIDC clients | `keycloak.clients` | See the gap in [SciNugget](cases/scinugget.md) (confidential clients) |
| Secrets | OpenBao KV, AppRoles with CIDR-bound SecretIDs, delivered wrapped | `storage.approles` | n/a |
| A public hostname | TLS, DNS and a reverse proxy at the edge | `web:` | n/a |
| A shared cache | A per-tenant Redis user on the `cache` stack | `stacks: [cache]` | `kv/tenants/<id>/cache` |
| Backups | restic snapshots of tenant databases | `backup` stack | n/a |

The `cache` stack is a **cache**: one Redis with `maxmemory 256mb` and
`allkeys-lru` (**Verified**, `stacks/cache`). It is the wrong place for a job
queue that needs `noeviction` (see the traps).

## 3. The steps

| # | Step | Who | Verified by |
|---|---|---|---|
| 0 | **Local parity.** The developer's own compose file mirrors the services the fleet will give (Postgres, Redis, S3, OIDC) | Dev | Their test suite passes against those services |
| 1 | **Dependency inventory.** Map every dependency of the app to a fleet service (table above); list what has no match | Dev + operator | A table with no blanks. Gaps are written down, not discovered later |
| 2 | **Declare the tenant.** `tenants/<id>.yaml`: realm, database, storage, AppRoles, web | Operator | `mksrv validate` |
| 3 | **Apply.** `mksrv plan --infra-only`, then `mksrv apply --infra-only` (the operator confirms the prompt), then `mksrv tenant apply <id>` | Operator | The plan is read first; `tenant apply` reports each piece; OpenBao shows the new names |
| 4 | **Deliver the identity.** `mksrv tenant secret-id <id> --approle <name>` yields a *wrapped*, single-use SecretID that only works from the declared node | Operator → dev | A login from the node succeeds; from anywhere else it is rejected |
| 5 | **The tenant's own secrets** (API keys of external services) go into OpenBao, written by the tenant admin with their OIDC login | Dev | `bao kv list` shows names, never values |
| 6 | **Dark launch.** The app runs on the node, listening **only on the tailnet address**, with no public hostname | Dev via the agent | The app's own health endpoint, a real login, a functional smoke test |
| 7 | **Migrations** run before the version switch (section 5) | Dev | The previous version still works against the new schema |
| 8 | **Publish.** Add `web:` pointing at the node; TLS and DNS come from the edge | Operator | The public URL answers; headers and CORS are right |
| 9 | **Operate.** Logs, metrics, alerts, backups, rotations (section 7) | Both | A dashboard, and a restore actually tried |

## 4. Deploying without a staging environment

Without a second environment, safety stops coming from "test it somewhere
else". It comes from four compensations, which are the point of steps 6 and 7:

1. **A local environment equivalent to the fleet** (step 0). The mismatch is the
   risk, so shrink it before the first deploy.
2. **A dark launch on the tailnet** (step 6). Same data, same services, no
   public exposure. This is the poor man's staging: the real thing, but only the
   team can see it.
3. **Reversible migrations** (step 7). Expand first, contract later, so the
   previous version keeps working after the schema moves.
4. **Instant rollback by reference.** The agent keeps the previous package and
   can `switch` back to it in seconds.

Checklist before step 8:

- [ ] No development bypass is enabled in the production environment.
- [ ] The health endpoint reflects real dependencies, not just "process up".
- [ ] A backup of the tenant database exists from before the first migration.
- [ ] The rollback has been **performed once**, not just described.
- [ ] Secrets are not in the package, the image, the logs or the git history.
- [ ] The dependency failure modes are known (what happens if Redis, S3 or the
      database is down) and acceptable.

## 5. Migrations

- Run them **before** activating the new version; they must be compatible with
  the version currently running (expand, then contract in a later release).
- Take a database backup first.
- The login role's session role defaults to the tenant's owner role, so objects
  created by migrations are owned correctly. A schema beyond `database.schema`
  needs a provisioning step run once and separately (`SET ROLE NONE`, then
  `CREATE SCHEMA`; see the tenant developer guide kept in the operator's
  workspace).
- If PostgREST exposes the schema, **end every migration with
  `NOTIFY pgrst, 'reload schema';`**, or new functions stay invisible with no
  error.

## 6. Rollback and recovery

- **App:** `switch` to the previous package reference. The migration being
  backward-compatible is what makes this safe.
- **Data:** restore the pre-migration backup into a scratch database first and
  check it, rather than into production.
- **Credentials:** SecretIDs expire (90 days by default); mint a new wrapped one
  and revoke the old by accessor (`tenant secret-id --list`, `--revoke`).

## 7. Operating it

| Need | Today |
|---|---|
| Logs | Local on the node. The fleet's Loki is **not** reachable from tenant nodes: the Headscale ACL does not open its port, and opening it to all tenants would let any of them read everyone's logs. A scoped rule or an authenticated endpoint is undecided |
| Metrics | The fleet's Prometheus does not scrape tenant nodes yet |
| Alerts | mksrv ships a rule catalog and no notification channel ([ADR 0022](../explanation/decisions/0022-alert-rules.md)) |
| Backups | restic covers the tenant databases; test a restore |

## 8. Known traps

- **An AppRole's CIDR binds the host, not the app.** Two apps on one node share an
  address, so the CIDR cannot tell them apart. Separation comes from a distinct
  AppRole and SecretID per app, and file permissions per service user.
- **OpenBao only sees real client addresses since [ADR 0042](../explanation/decisions/0042-openbao-host-networking.md).**
  Before it, tailscaled's masquerade made every tailnet client look like the
  podman bridge gateway, and CIDR-bound roles failed.
- **An explicit `kv_read` list replaces the default** (`storage-*`); repeat what
  you still need.
- **Wrapped SecretIDs expire** (2 h by default in our runs, single use): mint
  them when you are ready to place them.
- **Do not hand out the PostgREST authenticator** (`<id>_auth`): it can `SET ROLE`
  to every tenant role, admin included. Prefer a dedicated service role with the
  privileges its migration grants. It is publishable only on explicit opt-in
  ([ADR 0039 addendum](../explanation/decisions/0039-tenant-roles-postgrest-keycloak-clients.md)).
- **Postgres from a tailnet node depends on the masquerade.** The cluster's
  `pg_hba` admits tailnet clients only because they arrive as the bridge
  (`samenet`). Disabling Tailscale's SNAT fleet-wide would break them.
- **`restorecon -R` on a rootless service user's home outside `/home`** labels
  podman's storage `var_lib_t` and containers stop executing. Fix with a
  `semanage fcontext -a -e /var/lib/containers <home>/.local/share/containers`.
- **A queue is not a cache.** Job queues that require `noeviction` cannot share
  the `cache` stack's `allkeys-lru` Redis.

## 9. Known gaps in mksrv

Collected from the cases; each links to where it was found.

| Gap | Status | Case |
|---|---|---|
| Secret of a declared **confidential** OIDC client is not delivered to the tenant | **Verified** in code | [SciNugget](cases/scinugget.md) |
| A reusable deploy-agent kit (the HG-LIMS agent is tied to `web` and `worker`) | Open | both |
| Rendering OpenBao secrets into the environment of an app with no OpenBao client | Open | [SciNugget](cases/scinugget.md) |
| Logs and metrics from tenant nodes | Open | both |
| A leader-aware Postgres endpoint for clients that accept a single host | Designed and implemented, **not deployed** ([ADR 0044](../explanation/decisions/0044-postgres-leader-endpoint.md)) | [SciNugget](cases/scinugget.md) |
