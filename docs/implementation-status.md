# Implementation status

## M0 — implemented

- Repository, license, Makefile, GoReleaser, CI/release workflows.
- Embedded engine tree and atomic versioned cache extraction.
- Four draft 2020-12 JSON Schemas.
- Models for deployment, hosts, tenants, users, stacks, and lock files.
- Workspace discovery, loading, schema checks, and semantic checks.
- Functional `version`, `validate`, and `doctor` commands with JSON output.
- Synthetic workspace and automated tests.
- Terraform/module and stack/template directory scaffolding.
- Public-data hygiene script and gitleaks configuration.
- CLI on Cobra, YAML via `sigs.k8s.io/yaml`, schema validation via
  `santhosh-tekuri/jsonschema/v6` (ADR 0007); embedded `time/tzdata`.

## Explicitly not implemented in M0

No AWS APIs, Terraform execution, provider generation, SSH connection, host
bootstrap, template rendering, Quadlet activation, DNS mutation, Keycloak or
Headscale API calls, secret decryption, tenant mutation, user mutation, deploy,
status, logs, or destroy behavior exists yet.

## M1 entry gate

Closed on 2026-08-27. The temporary M0 dependency-free CLI, YAML, and schema
adapters were replaced with Cobra, `sigs.k8s.io/yaml`, and
`santhosh-tekuri/jsonschema/v6`; the existing black-box tests passed unchanged.
See ADR 0007 (which supersedes ADR 0006).

## M1 — in progress

Design decisions recorded:

- ADR 0008 — Terraform execution wrapper (`hashicorp/terraform-exec` +
  `hashicorp/hc-install`), Terraform pinned to `1.9.8` via `internal/tf.Version`.
  Go baseline raised to 1.25.
- ADR 0009 — `mksrv init` generates a private workspace scaffold.

Implemented:

- `internal/tf` — `Version`, `CacheDir`, `Locate` (MKSRV_TERRAFORM → cache → PATH
  → download), and `Runner` (`Init`, `Validate`, `Plan`, `Apply`, `Output`).
  Unit-tested offline; `init`/`plan`/`apply`/`output` covered by an
  `integration`-tagged test run in CI.
- `version` now reports the real pinned Terraform version.
- `internal/scaffold` + `mksrv init` — renders a private workspace from embedded
  templates, resolves required values from flags or prompts, refuses to
  overwrite `deployment.yaml` without `--force`, and validates the result
  (ADR 0009).
- Optional per-tenant `mail` block in `schemas/tenant.v1.json` / `model.Tenant`.
- ADR 0010 records the first real deployment's topology decisions.
- `internal/aws` — SDK client set, `WhoAmI`, `ExportEnv` (hands resolved
  credentials to Terraform), and idempotent S3 + DynamoDB state-backend bootstrap.
- `internal/infra` — decodes a validated workspace into
  `.mksrv/infra/mksrv.auto.tfvars.json` and the `-backend-config` entries.
- Terraform modules `network`, `aws-host`, `dns` and the `infra/root` wiring.
- `mksrv plan --infra-only` / `mksrv apply --infra-only`.

Still deferred: `existing-host` module contents, per-tenant and mail DNS
(`mail-ses`).

## M2 — implemented

- `internal/ssh` — SSH/SFTP transport, workspace-pinned `known_hosts`, explicit
  enrollment (`mksrv host trust`), `Run` / `RunScript` / `WriteFileSudo`.
- `internal/render` — text/template stack renderer keyed by descriptor
  destination path; typed `Context`.
- `internal/deploy` — idempotent marker-guarded Rocky 9 bootstrap; `DeployStack`
  with sha256-compared writes, Quadlet unit activation, and health checks.
- `base` stack templates (Caddy + podman network).
- `mksrv bootstrap`, `mksrv deploy`, `mksrv status`, and the full `mksrv apply`
  chain (`--trust-hosts` for first run).

Verified end to end against two live AWS hosts: bootstrap (SELinux enforcing,
firewalld, Podman/Quadlet, data volume mounted), `base` deployed, external
`/healthz` OK, `mksrv status` healthy, re-runs idempotent.

## M3 — implemented

- `internal/secrets` (SSM), `internal/headscale` (users, pre-auth keys).
- `internal/deploy`: podman-secret push, `post_deploy` hooks, `.deployed`
  markers, `mesh.go` (tailnet node join). Bootstrap v7.
- `identity` stack: Keycloak (its own Postgres), Headscale, Caddy vhost
  fragments; `mksrv mesh` joins fleet hosts and creates per-tenant Headscale
  users.

Verified live: `auth.<domain>` serves Keycloak over TLS, `vpn.<domain>/health`
passes, both fleet hosts on the tailnet with cross-host connectivity,
`mksrv status` healthy, deploy/mesh idempotent.

## M4 — implemented

- `internal/keycloak` — Admin REST client; `EnsureRealm` (realm, groups,
  PKCE + confidential clients), `EnsureUsers` (no pruning).
- `internal/configd` + `cmd/configd` — token verification against the realm
  JWKS, Headscale pre-auth key minting, Ed25519 compact-JWS clientconfig.
- `mksrv tenant apply` / `mksrv users apply`.

Verified live: `tenant apply` created the bitabit/mcps/hg realms; a Keycloak
password-grant token for realm bitabit fetched a clientconfig from
`https://cfg.<domain>/v1/clientconfig` that Cloud-IT VPN's `VerifyCompact`
accepts (tenant binding, freshness, structure all pass).

## M5 — implemented

- `database` stack: PostgreSQL 16 (published on the host private + tailnet IP)
  and pgAdmin. `mksrv tenant apply` provisions one DB, login role, and `app`
  schema per tenant.
- `monitor` stack: Prometheus, Grafana (provisioned Prometheus datasource),
  node-exporter, cAdvisor.
- Cross-host Caddy vhost fragments (edge serves `grafana.` / `pgadmin.`);
  `mksrv mesh` records tailnet IPs; intra-VPC security-group rule.

Verified live: `grafana.` and `pgadmin.` serve over TLS; `psql` as a tenant
role into its own database works; Postgres is reachable on the tailnet.

Still deferred: table-level RLS policies (application concern), per-tenant DNS
records under the real tenant domains, Grafana OIDC, mail (SES), a
GHCR-published configd image, signing-key rotation, and `mksrv destroy`.
