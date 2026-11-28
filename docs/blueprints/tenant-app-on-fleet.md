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

### Operator decisions (2026-11-27)

These are the operator's policy for new apps. mksrv itself does not implement or
enforce them: [ADR 0036](../explanation/decisions/0036-per-tenant-service-user-local-hosts.md)
stays *Proposed*, and the agent and its service user are the operator's tooling.

- **The socket dispatcher is the pattern for new apps.** Root is used once, to
  install it; routine deploys go through its verbs. A personal account with
  `sudo` is for that installation and for the person who deploys.
- **A contained entry session (sidecar) is mandatory when a person or a coding
  agent has an account on the host.**
- **Where it lives:** these documents here; the home of a reusable agent's code
  is not decided.

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
- **A Redis image with its default entrypoint may drop to a uid that cannot read a `0600` file of the
  service user** (and a first failed attempt can leave `redis-data` owned by a mapped uid). Run it with
  `UserNS=keep-id` and `User=<the service user's uid>`. *Reported by the SciNugget agent, seen on its node.*
- **Podman with OCI-format images ignores the Dockerfile `HEALTHCHECK`.** Define the health check in the
  Quadlet (`HealthCmd`). *Reported by the SciNugget agent.*
- **`sudo -u <svc> -H` inherits the caller's working directory**, and `podman` then fails with `cannot chdir`.
  `cd ~` first. *Reported by the SciNugget agent.*
- **Latency to the database is not local latency.** Through the leader endpoint a query costs on the order of
  100 ms or more, so Prisma's default 5 s interactive-transaction limit can expire in a script that was fine
  locally; raise it. Do the S3 upload *after* the transaction, or delete the object when the transaction fails:
  an upload first and a failed save leaves an orphan. *Reported by the SciNugget agent (its measurement).*
- **The tenant's S3 key cannot delete objects, on purpose** (see
  [ADR 0041](../explanation/decisions/0041-tenant-storage-s3-and-approles.md): deletion waits for retention
  classes). A delete from the app fails with `AccessDenied`; an app that tolerates it leaves orphans for the
  operator to clean. *Verified: the policy omits `DeleteObject` by design.*
- **A backup is not proven by the job's exit status.** Until 2026-11-22 the restic container could not read the
  staged dumps (no SELinux label under Enforcing) and restic's "some files unreadable" exit was treated as a
  warning: no snapshot held a database dump. Check a snapshot's contents (`restic ls`), and try a restore.
  The job now fails when staged files are not in the snapshot
  ([ADR 0018 addendum](../explanation/decisions/0018-restic-backups.md)). *Verified.*

- **Apps in one tenant are not isolated from each other.** There is one database
  and one bucket per tenant: whoever reads the `database` secret sees all of it,
  and `storage-s3` is one key for every object. Apps that need isolation go in
  separate tenants. *Verified in code.*
- **Overlapping `kv_read` prefixes leak between apps.** A role reading `app-*`
  also reads a second app's `app-<name>` secrets. Give each app its own prefix.
- **Removing an AppRole from the YAML does not delete it**, nor its SecretIDs.
  Likewise there is no command to [remove a tenant](../how-to/tenant-removal.md).
- **A tenant with no public hostname cannot sign in through the tailnet
  address.** A redirect URI is `https` under `base_domain`, or `http://localhost`
  with `allow_localhost`: test the dark launch through an SSH tunnel.
- **A plan code is not an approval.** A confirmation code derived from the plan
  proves that what runs is what was planned, not that a person agreed: whoever
  may ask for a plan gets the code. Approval comes from a per-caller verb list
  that denies `deploy` to the agent, and holds only if the agent cannot also use
  the person's credentials. *Reported by the SciNugget agent.*
- **Installing a sidecar can lock its account out.** The host may keep SSH keys
  centrally (`AuthorizedKeysFile`), or the developer may not arrive from the
  network you restrict to. Read the effective configuration
  (`sshd -T -C user=…,host=…,addr=…`) and `$SSH_CONNECTION`, and have the
  installer verify it and revert (`sshd -t` only checks syntax). *Reported by the
  SciNugget agent.*
- **A per-user egress block also blocks the image pull** of that user: build the
  image before loading the rule. *Reported by the SciNugget agent.*

## 9. Known gaps in mksrv

Collected from the cases; each links to where it was found.

| Gap | Status | Case |
|---|---|---|
| Secret of a declared **confidential** OIDC client is not delivered to the tenant | **Verified** in code | [SciNugget](cases/scinugget.md) |
| A reusable deploy-agent kit (the HG-LIMS agent is tied to `web` and `worker`) | Open | both |
| Rendering OpenBao secrets into the environment of an app with no OpenBao client | Open | [SciNugget](cases/scinugget.md) |
| Logs and metrics from tenant nodes | Open | both |
| A leader-aware Postgres endpoint for clients that accept a single host | Deployed (`url_single`, port 5433) ([ADR 0044](../explanation/decisions/0044-postgres-leader-endpoint.md)); a real failover and the added latency are **not yet measured** | [SciNugget](cases/scinugget.md) |
