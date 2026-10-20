# ADR 0012: `logs` and `security` stacks

- Status: Accepted
- Date: 2026-09-01
- Milestone: M8

## Context

The fleet has metrics (`monitor`) but no centralized logs and no perimeter
security tooling. Debugging is SSH + `journalctl`; the edge runs Caddy on public
80/443 and SSH from a rotating residential `mgmt_cidr` with no brute-force
protection.

## Decision

### `logs` — Grafana Loki + Alloy

- **Loki**, single-binary, **filesystem** storage (object storage is for large
  deployments; ~40 users is not one), 14-day global retention, `analytics`
  reporting off.
- **Grafana Alloy** collector reads the host **systemd journal**. Bootstrap
  `BootstrapVersion` 8 sets podman `log_driver = "journald"` and makes the
  journal persistent, so every container's stdout is in the journal keyed by
  `CONTAINER_NAME` / `_SYSTEMD_UNIT`. Streams carry `unit`, `container`, `level`,
  `host`, `env` labels.
- **Tenant scoping is by label** (`container=~"mksrv-postgrest-<id>"`), not Loki
  multi-tenancy (`X-Scope-OrgID`). Real per-tenant tenancy + per-tenant retention
  is deferred.
- Grafana gains a `Loki` datasource, provisioned only when a host carries `logs`.
- **v1 runs on the monitor host only** — it collects the data-plane journal. Edge
  log collection into Loki (Alloy on the edge) is deferred; CrowdSec on the edge
  reads the edge journal directly.

### `security` — CrowdSec + firewall bouncer

- **CrowdSec Security Engine** parses sshd auth and the Caddy JSON access log
  (added to `stacks/base/templates/Caddyfile.tmpl`) from the journal. Collections
  `crowdsecurity/{linux,sshd,caddy}`. **CAPI-enrolled** — pulls the community
  blocklist and shares attack metadata (IPs + scenario names, not payloads).
  Prometheus metrics on `:6060`.
- **`cs-firewall-bouncer`** in a `Network=host` + `NET_ADMIN` container, **nftables
  mode** with its own `crowdsec` / `crowdsec6` tables that coexist with
  firewalld's table. Engine decisions become packet DROPs on the edge. The
  bouncer is pre-registered via `BOUNCER_KEY_firewall` (a `security` stack secret,
  the same value in the bouncer config).
- `per_tenant: false` — fleet-level. Per-tenant dashboards/reports are optional
  and deferred.
- Runs on the **edge** (the exposed host).

### `StackIP` render helper

`render.Context` gains `StackHosts` (stack name → private VPC IP of its carrier
host) and `StackIP(name)`. Cross-host templates (Alloy → Loki, Prometheus →
CrowdSec) use it instead of guessing host names via `.Peer`.

### Deploy-path secret rendering

`deploy.DeployStack` now copies `Options.Secrets` into `Context.Secrets` before
rendering, so a `shared` template can reference `{{ .Secrets.<leaf> }}` (already
relied on by `stacks/cache/templates/users.acl.tmpl`).

## Consequences

- Both stacks are **opt-in** — not in the workspace `deployment.yaml`. Deploying
  both pushes `data` and `edge` past 2 GB; the operator bumps them to
  `t4g.medium` (+~$24/mo).
- `BootstrapVersion` 8 re-runs the idempotent bootstrap sections once on the next
  `mksrv apply` for every host.
- `base` must be redeployed for the Caddy access log (`mksrv deploy edge --stack base`).
- Deferred: edge log collection into Loki, per-tenant Loki retention, CrowdSec
  Grafana dashboard auto-provisioning, CrowdSec AppSec/WAF and the Caddy bouncer
  (which would need a custom Caddy image).
