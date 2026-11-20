# Case: SciNugget

A Next.js application for scientific narrative coherence, traceability and
bibliography: it models Hypothesis → ExperimentDesign → Run → Evidence →
Conclusion and links to the outputs of external tools (it does not run
experiments). Status: **in progress, nothing deployed yet**. Follows the
[generic blueprint](../tenant-app-on-fleet.md).

Decisions already taken: it runs on **`ome`**, as its **own tenant**
(`scinugget`).

## 1. Dependency inventory (step 1)

Read from the app's `README.md`, `Dockerfile`, `ARCHITECTURE.md` and
`.env.example` (variable **names** only).

| Component | Fleet service | Status |
|---|---|---|
| PostgreSQL via Prisma, own migrations (`prisma migrate deploy`), 11 enums, no extensions | `database` with `postgrest: false` | Exists. Connection-string question: **G3** |
| Auth.js with Keycloak, requires a client secret | Realm and declarative client | **G1** |
| BullMQ on Redis | Not the `cache` stack | **G2** |
| S3-compatible `StorageAdapter` (S3, MinIO, local) | `storage.s3` | Exists. Verify the empty-endpoint path for AWS |
| Anthropic API (coherence check) and `git ls-remote` | Direct egress from `ome` | Exists. The app's own list asks for an egress proxy: **G7** |
| Web on port 3000 and a worker | Two containers | **G4** |
| Public hostname | `web:` | Exists |

The app's own `ARCHITECTURE.md` §11 lists what it still lacks for production: a
real secret manager, an egress proxy with IP pinning, allowlists, a specific CSP,
rate limiting, antivirus for PDFs, audit logs, key rotation and end-to-end TLS.
The fleet already covers part of that (OpenBao, TLS at the edge, rotation).

## 2. The tenant (step 2)

**Verified** with `mksrv validate` in a scratch workspace. The hostname, bucket
and port are placeholders to be chosen.

```yaml
version: 1
id: scinugget
display_name: "SciNugget"
base_domain: scinugget.example.com
keycloak:
  realm: scinugget
  clients:
    - id: scinugget-web
      public: false          # Auth.js here requires a client secret (G1)
      redirect_uris: [https://scinugget.example.com/api/auth/callback/keycloak]
      post_logout_redirect_uris: [https://scinugget.example.com/]
stacks: [database, openbao]  # no `cache`: the queue's Redis is not the fleet cache (G2)
device_limit: 5
database:
  postgrest: false           # an ORM straight to Postgres; no REST API, no public URL for it
  schema: scinugget
  connection_limit: 20
storage:
  s3:
    bucket: scinugget-prod-data
    cors_origins: [https://scinugget.example.com]
  approles:
    - name: app              # role svc-scinugget-app
      cidrs: [100.64.0.11/32]
      kv_read: ["database", "storage-s3", "app-*"]
web:
  - hostname: scinugget.example.com
    target: 100.64.0.11:8201   # step 8 only; omit during the dark launch
```

## 3. Environment variables → source

The app reads plain environment variables and has no OpenBao client (**G9**).

| Variable | Source |
|---|---|
| `APP_URL`, `AUTH_URL`, `AUTH_TRUST_HOST` | Configuration (public URL; trust the proxy) |
| `AUTH_SECRET` | `kv/tenants/scinugget/app-auth` (the tenant admin writes it) |
| `AUTH_DEV_BYPASS*` | Not set. The README says it is forced off when `NODE_ENV=production`; the checklist still requires it |
| `DATABASE_URL` | `kv/tenants/scinugget/database` → `url`, plus `?schema=scinugget` (**G3**, **G8**) |
| `KEYCLOAK_ISSUER` | `https://auth.example.com/realms/scinugget` |
| `KEYCLOAK_CLIENT_ID`, `KEYCLOAK_CLIENT_SECRET` | `scinugget-web`; the secret per **G1** |
| `REDIS_URL`, `QUEUE_DISABLED`, `WORKER_CONCURRENCY` | A Redis owned by the app's own package on the node (**G2**) |
| `S3_*` | `kv/tenants/scinugget/storage-s3` (region and bucket are configuration) |
| `LOCAL_STORAGE_*` | Unused with the S3 driver |
| `ANTHROPIC_API_KEY`, `ANTHROPIC_MODEL` | `kv/tenants/scinugget/app-anthropic` (the tenant admin writes it) |
| `ALLOW_PRIVATE_EXTERNAL_URLS` | `false` |

## 4. Gaps found before deploying

| ID | Gap | Status | What to do |
|---|---|---|---|
| **G1** | Auth.js enables Keycloak only when a client **secret** exists, so the client must be confidential. mksrv creates declared confidential clients but does **not** deliver their secret (`EnsureClient` returns it only to OpenBao's own OIDC setup and the SSO gate; the declarative reconcile never reads it) | **Verified** in code | Workaround: the tenant admin reads it once from the Keycloak admin console and stores it in `kv/tenants/scinugget/app-oidc`. Proposal: mirror it automatically for confidential declared clients, opt-in |
| **G2** | BullMQ needs Redis with `noeviction`. The `cache` stack is `allkeys-lru`, 256 MB, shared | **Verified** config; BullMQ's requirement is from its documentation | Run a dedicated Redis in the app's package on `ome`. No mksrv change |
| **G3** | The cluster's `database` secret has several hosts and `target_session_attrs=read-write` (libpq style). Prisma's engine may accept only one host | **To verify** | First spike: connect from `ome` with the real string. If unsupported: a driver adapter, or a leader-aware local proxy (Patroni's REST port is **not** in the tenants' ACL), or a stable leader endpoint from mksrv |
| **G4** | The `Dockerfile` starts only `server.js`; the worker runs with `tsx` and an `.env` file | **Verified** | A second image or entrypoint for the worker; the package carries `web/` and `worker/` |
| **G5** | A reusable deploy-agent kit: the HG-LIMS one knows only `web` and `worker` | Open | Extract the common parts (socket, closed verbs, content-addressed package, rollback) and leave the app-specific parts as configuration |
| **G6** | `ome` belongs to tenant `hg`'s tailnet identity. SciNugget running there reaches the fleet as `hg@`, and shares the node address with HG-LIMS, so the CIDR cannot separate them | **Verified** model | A distinct AppRole and SecretID per app, and a distinct service user per app |
| **G7** | The app lists an egress proxy and domain allowlists as pending | Open | Out of scope for the first deploy; track it |
| **G8** | The login role's session role defaults to the owner role; Prisma creates 11 enums and its own migration table in the target schema | **To verify** | Spike: `prisma migrate deploy` against `db_scinugget` with `?schema=scinugget` |
| **G9** | No OpenBao client in the app, so secrets must reach it as environment | Open | The agent renders a `0600` env file from OpenBao at start (never printing it), or the app learns to resolve secrets itself |

## 5. Plan

| Step | SciNugget-specific content |
|---|---|
| 0 | The repo's compose already mirrors Postgres, Keycloak, Redis and MinIO |
| 1 | The table above |
| 2 | The tenant file above, validated; choose hostname, bucket and port |
| 3 | `plan`, `apply --infra-only`, `tenant apply scinugget` |
| 4 | Wrapped SecretID for `svc-scinugget-app`, placed on `ome` |
| 5 | The tenant admin writes `app-auth`, `app-anthropic`, `app-oidc` |
| 6 | Dark launch on `100.64.0.11:<port>`: `/api/health`, a real Keycloak login, creating one full chain of nuggets |
| 7 | `prisma migrate deploy` before activation, after a backup. The current schema has two migrations |
| 8 | Add `web:` and apply |
| 9 | Logs and metrics are open; test a database restore |

Spikes first (they can change the design): **G3** and **G8**. They need nothing
deployed beyond the tenant and a database.

## 6. Open questions

1. Hostname, and the bucket name.
2. Who writes the tenant's secrets (the developer, as tenant admin).
3. Is the worker's Git enrichment (`git ls-remote`) allowed to reach arbitrary
   remotes from `ome`, or only an allowlist?
