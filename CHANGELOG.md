# Changelog

## Unreleased

- Fix: `infra/root/.terraform.lock.hcl` is now committed and embedded (`assets.go`
  gains an explicit embed for it, since bare `//go:embed infra` skips dotfiles).
  Without it every `mksrv apply` on a `dev` engine re-resolved the AWS provider
  from scratch. The lock pins `hashicorp/aws` with multi-platform hashes.

- Fix (M21): `backup.sh` never initialised the restic repository, so the very
  first run failed with "Is there a repository at the following location?". It
  now runs `restic init` when `restic cat config` shows no repo.
- Fix (M21): `mksrv-backup.service` `ExecStart`ed `backup.sh` directly, but the
  script lives under `/var/lib/mksrv` (relabelled `container_file_t` for podman)
  and systemd cannot exec it there — `status=203/EXEC`, Permission denied. Now
  `ExecStart=/usr/bin/bash …`.

- Fix: the Keycloak admin token expires (60s master-realm default) partway
  through a long `mksrv tenant apply`; the client now re-authenticates once on
  a 401 and retries.
- Fix (M13): `bao policy write <name> -` read empty stdin because `baoExec`
  built `podman exec` without `-i`, so `mksrv tenant apply` failed at the
  per-tenant OpenBao step ("'policy' parameter not supplied or empty").
- Fix (M5): `mksrv tenant apply`'s pgAdmin server-list load wrote the file
  inside the container and then `podman cp`'d it from the host, which fails.
  Now `tee`s on the host first.

- Fix (M20): `mksrv postgres bootstrap` recorded `.mksrv/postgres.json`'s
  `primary` as the Patroni IP, but `pgConn` (used by `mksrv tenant apply`) looks
  it up as a fleet host name — `mksrv tenant apply` then failed with
  "postgres primary "10.20.x.x" is not a fleet host". Now records the host name;
  `pgConn` also tolerates an IP from an older file.
- Fix (M11): dedicated-volume bind mounts (`prometheus` tsdb, `loki` chunks,
  `patroni` pgdata/raft, `openbao` baoraft) now use `:Z,U` so podman chowns the
  freshly-formatted XFS mount to the container's user. Without it Prometheus
  (uid 65534), Loki, Patroni and OpenBao exited on "permission denied" writing
  to their root-owned data dir.
- `mksrv deploy` now pulls a stack's images (`podman pull`, retried) before
  starting its units, so a slow first pull can't trip `TimeoutStartSec` /
  the restart rate limiter (which failed Grafana's 547 MB image on a fresh host).
- Fix (M20): the `database` stack's `postgres` TCP health check probed
  `127.0.0.1:5432` on the app host — nothing there in cluster mode. Removed; the
  standalone container gates on its own `pg_isready` HealthCmd and the cluster
  is gated by `mksrv postgres bootstrap`.
- Fix (M20): `pgadmin` / `postgrest` container units hard-coded
  `Requires=`/`After=mksrv-postgres.service`. In cluster mode that unit doesn't
  exist on the app host, so `systemctl restart mksrv-pgadmin.service` failed
  with "Unit mksrv-postgres.service not found". The dependency is now guarded by
  `{{ if not (.StackIP "postgres") }}`.
- Fix: the `cache` stack's `users.acl` seed carried a leading comment block.
  Redis's external `aclfile` parser accepts only `user …` and blank lines, so
  Redis aborted startup ("Aborting Redis startup because of ACL errors … should
  start with user keyword"). The seed template is now comment-free.
- Fix (infra): the `aws-host` module's `openbao_kms` / `backup_s3` IAM policy
  `count` depended on the KMS-key / S3-bucket ARN (unknown until apply), which
  broke the first real `apply` of a fleet with `openbao` assigned. `count` now
  keys off plan-time booleans (`openbao_kms_enabled` / `backup_enabled`).

## v0.1.0 — 2026-09-03

First tagged release. Everything below (M0–M21) ships in `v0.1.0`: the CLI +
embedded Terraform, the stack catalog (`base`, `identity`, `mail`, `database`,
`postgres`, `openbao`, `cache`, `monitor`, `logs`, `security`, `backup`, and the
`files` / `analytics` descriptors), the mesh / configd VPN broker, per-tenant
RBAC (`admin` / `dev` / `apps` / `vpn`), HA Postgres + OpenBao clusters, per-stack
storage + retention, and restic backups. `mksrv-configd` and `mksrv-postgres`
images are pinned to `:v0.1.0`.

## Unreleased — M21

- Added the `backup` stack (ADR 0018): restic → S3, daily on a systemd timer.
  Captures `pg_dump` of every tenant DB from the Patroni primary, an OpenBao
  raft snapshot, Keycloak realm exports, and the stack / podman volumes.
  Terraform creates a versioned, encrypted `mksrv-<env>-backups` bucket and
  grants the backup host's instance role S3 access (no keys — IMDS). The restic
  password is a podman secret; the OpenBao token is a raft-snapshot-only
  periodic token. `mksrv backup run` / `mksrv backup list`; restore is
  documented (`docs/backup.md`). `mksrv tenant apply` writes `backup.env`.

## Unreleased — M20

- `database` runs on the Patroni cluster when a `postgres` cluster is in the
  fleet (ADR 0017), auto-detected from `.mksrv/postgres.json`. Standalone
  Postgres is kept for single-host / `local` dev. `provisionDatabases` targets
  the Patroni primary; PostgREST connects with a libpq multi-host DSN
  (`target_session_attrs=read-write`); pgAdmin registers the primary IP.
- A stack template that renders to whitespace is now dropped
  (`deploy.dropEmpty`), so `postgres.container.tmpl` self-disables when a cluster
  exists.
- `stacks/postgres` `pgdata` volume: 40 GiB @ 4000 IOPS → 20 GiB baseline.

## Unreleased — M19

- Postgres access per RBAC group (ADR 0016), completing the model. `mksrv
  tenant apply` provisions `<id>_app` (SELECT on `app` by default; the dev opens
  writes per table) and `<id>_web` alongside `<id>` / `<id>_anon` / `<id>_auth`.
  The `role` claim is now `<id>_web` (one hardcoded mapper) and an
  `app.pgrst_pre_request` function narrows it — `admin`/`dev` → `<id>`, `apps` →
  `<id>_app`, token-less → `<id>_anon` — from the token's `groups` claim.
  PostgREST gains `PGRST_DB_PRE_REQUEST`.
- On an existing realm, delete the `mksrv-role` protocol mapper once so
  `tenant apply` recreates it with the `_web` value.

## Unreleased — M18

- OpenBao access is now per RBAC group (ADR 0016). `mksrv tenant apply` writes
  two policies per tenant — `tenant-<id>-admin` (full KV control incl. version
  destroy, Transit key rotate) and `tenant-<id>-dev` (read-only KV, read/write
  `kv/tenants/<id>/dev/*`, Transit encrypt/decrypt) — and two OIDC roles bound
  on the `groups` claim: `tenant-<id>-dev` (`dev` or `admin`, the default) and
  `tenant-<id>-admin` (`admin`, `-role=`-selected). The AppRole (services) is
  repointed to `tenant-<id>-dev`. `apps`/`vpn`-only members can no longer log
  into OpenBao. The pre-RBAC `tenant-<id>` policy/role is removed best-effort.

## Unreleased — M17

- Per-tenant RBAC groups `admin` / `dev` / `apps` / `vpn` (ADR 0016, `docs/rbac.md`).
  `mksrv tenant apply` creates the four groups, adds an
  `oidc-group-membership-mapper` (`groups` claim) to the `cloud-it-vpn-desktop`
  and `openbao` clients, and grants the `admin` group Keycloak's fine-grained
  team-management roles (`manage-users` / `query-users` / `query-groups` /
  `view-users`) so a tenant admin runs their own users from the console.
- configd now requires a VPN-enabled group: `/v1/clientconfig` returns 403
  unless the token's `groups` claim contains `vpn`, `dev`, or `admin`.
  `keycloak.RealmSpec.AdminGroup` / `keycloak.ClientSpec.GroupsClaim`.
- OpenBao per-group policies (M18) and the Postgres `<id>_app` role / group-derived
  `role` claim (M19) are follow-ups; until then every authenticated tenant user
  keeps `role: <id>`.

## Unreleased — M16

- `mksrv tenant apply` mirrors each tenant's own Postgres and Redis connection
  secrets into `kv/tenants/<id>/database` and `kv/tenants/<id>/cache` (for
  tenants that consume those stacks and `openbao`), so a team reads its
  credentials with its AppRole/OIDC token instead of via the operator. SSM
  stays mksrv's source of truth; the mirror is only rewritten when the stored
  value has drifted, keeping KV version history quiet.

## Unreleased — M15

- Human login to OpenBao via Keycloak OIDC (ADR 0015 M15 update). For each
  tenant that consumes `openbao`, `mksrv tenant apply` adds a confidential
  `openbao` OIDC client to the tenant's realm (loopback callback
  `http://localhost:8250/oidc/callback`) and a per-tenant `oidc-<id>/` auth
  mount pointed at the realm, with a `tenant-<id>` role bound to the
  `tenant-<id>` policy. `bao login -method=oidc -path=oidc-<id>`.

## Unreleased — M14

- OpenBao Transit engine for PII-column encryption (ADR 0015 M14 update).
  `mksrv openbao bootstrap` now also enables `transit/`. `mksrv tenant apply`
  creates a non-exportable `transit/keys/<id>` (`aes256-gcm96`, 90-day
  auto-rotate) per tenant that consumes `openbao`, and the `tenant-<id>` policy
  gains `transit/{encrypt,decrypt,rewrap,datakey/plaintext}/<id>` (update) and
  `transit/keys/<id>` (read). Apps encrypt PII via the API; the key never leaves
  the cluster.

## Unreleased — M13

- Per-tenant OpenBao (ADR 0015 M13 update). The `openbao` stack is now
  `per_tenant: true` — a tenant opts in by listing `openbao` in its `stacks:`.
  `mksrv tenant apply` reconciles, per consuming tenant: a `tenant-<id>` policy
  scoped to `kv/tenants/<id>/*`, an AppRole bound to it, and its RoleID/SecretID
  in SSM (`/mksrv/<env>/openbao/approle_<id>_{role_id,secret_id}`, SecretID
  written once). The reconcile no-ops until `mksrv openbao bootstrap` has run.

## Unreleased — M12

- Added the `openbao` stack (opt-in, `kind: cluster`, ADR 0015): an OpenBao
  cluster on Integrated Storage (Raft) with **AWS KMS auto-unseal**. Terraform
  creates a fleet KMS key (`alias/mksrv-<env>-openbao`) and grants
  `kms:Encrypt`/`Decrypt` only to hosts carrying the stack; the listener is
  plaintext behind the mesh/VPC (as Patroni). A dedicated `baoraft` gp3 volume
  backs the raft store.
- `mksrv openbao bootstrap` — initialises the cluster, writes the recovery keys
  and root token to SSM (`/mksrv/<env>/openbao/{recovery_keys,root_token}`),
  waits for auto-unseal + a converged raft, and enables **KV v2** and
  **AppRole**. `mksrv openbao status` reports seal/raft/engine state.
- `infra.HostOutput` gains `openbao_kms_key_id`; `render.Context` gains `Region`
  and `OpenBaoKMSKeyID`. Headscale's fleet service-port ACL gains `8200`.
- Per-tenant policies/AppRoles, Transit (PII), PKI, and OIDC-via-Keycloak are
  M13.

## Unreleased — M11

- Per-stack dedicated EBS volumes (ADR 0014): `stack.yaml` `storage: [{name, gb,
  iops, throughput, grows_with}]`. The CLI aggregates a host's volumes, Terraform
  attaches a gp3 volume per entry (provisioned IOPS/throughput when set), and the
  bootstrap (`BootstrapVersion` 10) matches disks by EBS volume id and mounts each
  at `/var/lib/mksrv/vol/<name>`. Templates use `{{ .Volume "<name>" }}`.
  `postgres`, `monitor`, and `logs` adopt it.
- `deployment.yaml` `retention: {metrics_days, logs_days, metrics_gb_per_day,
  logs_gb_per_day}` — feeds the Prometheus / Loki retention flags (were
  hardcoded) and the `grows_with` volume sizing.
- `mksrv host migrate-volume <host> <stack> [name...]` — copies a stack's
  named-volume data onto its dedicated EBS volume for an existing deployment.

## Unreleased — M10

- Capacity checks: `mksrv validate` now emits a `capacity.overcommit` **warning**
  (validity unaffected) when an AWS host's stacks' `resources.min_ram_mb` sum
  exceeds the instance memory. `internal/model` gains `InstanceRAMMB` (a table of
  the t/m/c/r families) and `SwapForStacks`.
- Adaptive swap: swap moves from first-boot Terraform (`swap_mb`, never
  reconciled) to the re-runnable bootstrap script (`BootstrapVersion` 9). The
  size is derived per host from `sum(min_ram_mb) + 512 headroom − instance RAM`,
  rounded to 512, capped at `min(RAM, 4 GiB)`. `aws_instance` sets
  `user_data_replace_on_change = false` so the swap removal is not a replacement.
- New `docs/capacity.md` — the sizing model and a per-instance-type table for the
  distributed profile.

## Unreleased — M9

- Added `kind: cluster` stacks (ADR 0013): a stack that must be assigned to an
  odd number of hosts ≥ 3 and self-organises a quorum. `render.Context` gains
  `StackMembers` / `.StackNodes` / `.StackPeers`.
- Added the `postgres` stack (opt-in): Patroni-managed HA PostgreSQL 16 with a
  raft DCS (no etcd), automatic failover, image `ghcr.io/<owner>/mksrv-postgres`
  built in CI. `mksrv postgres bootstrap` waits for the cluster, records
  `.mksrv/postgres.json`, creates the `app` database, and can force a switchover.
- `infra/modules/network` gains `subnet_count` (multi-AZ subnets, `moved` blocks
  keep the live single subnet). The CLI sets it to 3 when a `kind: cluster` stack
  is assigned. AWS hosts round-robin across the AZ subnets.

## Unreleased — M8

- Added the `logs` stack (Grafana Loki + Alloy journal collector) and the
  `security` stack (CrowdSec engine + `cs-firewall-bouncer`), both opt-in
  (ADR 0012). Loki uses filesystem storage and label-based tenant scoping;
  CrowdSec is CAPI-enrolled and enforces bans via its own nftables tables.
- `stacks/base` Caddyfile now emits a JSON access log (for Loki and CrowdSec).
- `render.Context` gains `StackHosts` + `StackIP(name)` — the private IP of the
  fleet host carrying a stack, for cross-host templates.
- Bootstrap `BootstrapVersion` 8: podman `log_driver = "journald"` + persistent
  journal, so all container stdout is collectable from the journal.
- Fix: `deploy.DeployStack` now exposes resolved stack secrets to `shared`
  templates as `{{ .Secrets.<leaf> }}` (previously only per-tenant reconcilers
  set `Context.Secrets`).

## Unreleased — M7

- Tenant-owned infrastructure (ADR 0011). Three optional tenant-document blocks:
  - `forwards` — per-tenant Cloud-IT VPN forwards (`http`/`tcp`/`ssh`), appended
    to the configd roster after the built-in fleet forwards. No `configd` or
    `cloud-it-vpn` change.
  - `dns` — A/AAAA/CNAME records written into the tenant's own Route53 zone
    (`dns_override.zone_id`) via a new `module "dns_tenant"`, with
    `allow_overwrite = false` so mksrv never clobbers a record it did not create.
  - `mesh_routes` — CIDRs a tenant's nodes may advertise; each gets a Headscale
    ACL rule (route approval stays manual).
- `mksrv tenant mesh <id>` — mints a Headscale pre-auth key under the tenant's
  Headscale user and prints the `tailscale up` command for a tenant-owned node.
- `headscale.Policy` now takes `[]PolicyTenant` (id + routes) instead of `[]string`.

## Unreleased — M6

- Added the `cache` stack: one shared Redis 7 on the data host, with a
  per-tenant ACL login user confined to the `<id>:*` key and channel namespace
  (no `@dangerous`/`@admin` commands). `mksrv tenant apply` rewrites the ACL
  file and runs `ACL LOAD`; passwords land in SSM
  `/mksrv/<env>/cache/redis_<id>_password`. Reachable over the tailnet and via a
  `cache` forward in the configd roster; Headscale ACL opens port 6379.
- Added PostgREST to the `database` stack: `mksrv tenant apply` now deploys one
  `mksrv-postgrest-<id>` container per tenant that consumes `database`, connected
  as an `<id>_auth` authenticator role that switches into `<id>` (JWT `role`
  claim) or `<id>_anon`. Tokens are verified against the tenant realm's JWKS; a
  hard-coded `role` claim mapper is added to the `cloud-it-vpn-desktop` client.
  Each instance is exposed at `<id>.rest.<root_domain>` (edge Caddy vhost + an
  operator DNS record) and over the tailnet, with a `rest` forward in the
  configd roster. Headscale ACL opens ports 3010-3019.
- Added `mksrv destroy --infra-only` (confirmation-gated `terraform destroy`).
- Added a GitHub Actions workflow that builds and pushes a multi-arch
  `ghcr.io/<owner>/mksrv-configd` image; the `identity` stack now references it.
- `internal/keycloak` gains `EnsureClient` (single-client reconcile returning
  the client secret).
- Added a Spanish quick-start (`docs/quickstart.es.md`).

## Unreleased — M5

- Added the `database` stack (PostgreSQL 16 + pgAdmin) and the `monitor` stack
  (Prometheus, Grafana, node-exporter, cAdvisor). `mksrv tenant apply` now
  provisions a database, login role, and `app` schema per tenant.
- Cross-host Caddy vhosts: rendered files under `/var/lib/mksrv/caddy.d/` from a
  non-edge host's stack are written to the edge and Caddy is reloaded.
- `render.Context` gains `Peers` (fleet private IPs), `Host.TailnetIP`;
  `mksrv mesh` writes `.mksrv/mesh.json`. Caddy admin moves to `127.0.0.1:2019`.
- `aws-host` gets an intra-VPC ingress rule; two operator DNS records
  (`grafana.`, `pgadmin.`).

## Unreleased — M4

- Added `internal/keycloak` (Admin REST client — realms, groups, OIDC clients,
  declarative users) and `internal/configd` + `cmd/configd` (the Cloud-IT VPN
  broker: verifies a Keycloak RS256 token, mints a one-use Headscale pre-auth
  key, returns a compact Ed25519 JWS clientconfig). Containerfile + goreleaser
  `configd` binary.
- New commands `mksrv tenant apply` (realms, groups, clients, mesh users, and
  the configd unit + signing key + roster) and `mksrv users apply`.
- `internal/secrets` gains `Put` / `EnsureString`; `internal/headscale` gains
  `CreateAPIKey`; stack deploy runs `podman network reload --all` after
  container restarts.
- The `identity` `configd` app now points at `localhost/mksrv-configd:dev`; the
  Caddy fragment serves the `cfg.` vhost.

## Unreleased — M3

- Added `internal/secrets` (SSM Parameter Store resolver; `EnsureRandom`
  generates a SecureString on first use) and `internal/headscale` (users and
  pre-auth keys via the container CLI).
- `internal/deploy` gains podman-secret push, `post_deploy` hook execution,
  per-stack `.deployed` markers, and `mesh.go` (tailnet node join). Bootstrap v7:
  relocated-graphroot SELinux equivalence, `tun` + netfilter modules.
- Added the `identity` stack: `mksrv-identity` network, a dedicated Postgres,
  Keycloak, Headscale, a Caddy vhost fragment, and a reload hook. Caddy moves to
  host networking and imports `caddy.d/*.caddy` fragments.
- New command `mksrv mesh`: reconciles Headscale users (one per tenant plus a
  fleet user) and joins every fleet host to the tailnet.
- `scripts/public-hygiene.sh` allows well-known public DNS resolvers and the
  RFC 6598 (Tailscale CGNAT) range.

## Unreleased — M2

- Added `internal/ssh` (SSH + SFTP transport, workspace-pinned known_hosts,
  explicit first-use enrollment), `internal/render` (text/template stack
  renderer), and `internal/deploy` (idempotent Rocky 9 bootstrap + checksum-aware
  stack deploy with Quadlet activation and health checks).
- Added the `base` stack templates (Caddy Quadlet + Caddyfile + podman network).
- New commands: `mksrv host trust`, `mksrv bootstrap`, `mksrv deploy`,
  `mksrv status`. `mksrv apply` without `--infra-only` now bootstraps and
  deploys the fleet (base host first); `--trust-hosts` auto-enrolls on first run.

## Unreleased — M1

- Added `mksrv plan --infra-only` and `mksrv apply --infra-only`: resolve AWS
  credentials, bootstrap the S3 + DynamoDB Terraform state backend
  (`internal/aws`), decode the workspace into Terraform variables
  (`internal/infra`), and run the Terraform root. `apply` writes
  `.mksrv/infra/outputs.json` and updates `mksrv.lock`.
- Filled the Terraform modules: `network` (dedicated single-subnet VPC, no NAT),
  `aws-host` (Rocky 9 arm64 EC2, gp3 data volume, EIP, locked-down security
  group, SSM instance profile, minimal cloud-init), `dns` (Route 53 records),
  and `infra/root` wiring (hosts + operator-zone DNS).
- `internal/aws` exports the resolved credentials as `AWS_*` for Terraform, so
  credential sources newer than Terraform's embedded SDK work.
- `scripts/public-hygiene.sh`: no longer flags templated/service ARNs or HCL
  expression references in Terraform; allows the public Rocky AMI owner id.
- Added `mksrv init`: scaffolds a private workspace (`deployment.yaml`, `tenants/`,
  `.gitignore`, `.mksrv/`, `README.md`) from embedded templates, with flags or
  interactive prompts, `--force`, `--json`, and a validation pass on the result.
  New `internal/scaffold` package. ADR 0009.
- Added an optional per-tenant `mail` block (`domains`, `inbound`, `dmarc_rua`) to
  `schemas/tenant.v1.json` and `model.Tenant`, to drive per-domain SES identity in
  the `mail` stack.
- Recorded ADR 0010: operator domain plus per-tenant DNS overrides, the
  edge/data host split, SSM+age secrets, and a minimal `configd`.
- Added `internal/tf`: pinned Terraform version (`1.9.8`), binary location and
  download via `hashicorp/hc-install`, and a `Runner` over
  `hashicorp/terraform-exec` (`Init`, `Validate`, `Plan`, `Apply`, `Output`).
  ADR 0008.
- `mksrv version` now reports the real pinned Terraform version.
- Recorded ADR 0009: `mksrv init` will generate a private workspace scaffold.
- Raised the Go baseline to 1.25 (required by current `hc-install`); CI matrix is
  now `1.25.x` and `staticcheck` is `v0.8.1`.

## Unreleased — M0

- Added public/private engine-workspace boundary and repository scaffolding.
- Added embedded engine cache and seven stack descriptors.
- Added four JSON Schemas and schema-plus-semantic workspace validation.
- Added `version`, `validate`, and `doctor` with JSON output.
- Added synthetic examples, tests, release automation, and public-data hygiene.
- Replaced the temporary offline adapters with Cobra, `sigs.k8s.io/yaml`, and
  `santhosh-tekuri/jsonschema/v6`; behavioral tests unchanged (ADR 0007).
- Embedded `time/tzdata` so `-trimpath` builds validate timezones without
  system zoneinfo.
