# Changelog

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
