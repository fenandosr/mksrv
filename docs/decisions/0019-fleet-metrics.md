# ADR 0019: fleet-wide metrics (`agent` stack, central Prometheus scrape)

- Status: Accepted
- Date: 2026-09-04
- Milestone: M23 (phase 1 of 4)

## Context

The `monitor` stack (Prometheus + Grafana + node-exporter + cAdvisor) was a
single bundle assigned to one host. Its `prometheus.yml` only scraped its own
container-network node-exporter and cAdvisor, so the moment `monitor` runs on
one host of a multi-host fleet (the norm since M13's distributed profile), the
other hosts — including `postgres`/`openbao` cluster nodes and the public
`edge` — have no metrics at all. That is the fleet's biggest observability
blind spot: the Patroni/OpenBao quorum and the public edge are invisible.

The fix does not need Terraform: `infra/modules/aws-host` already opens
`aws_vpc_security_group_ingress_rule.intra_vpc` (all TCP within the VPC CIDR),
so any host can already reach any other host's private IP on any port. The
gap was purely that node-exporter/cAdvisor were never *published* on the
private IP, and nothing pointed Prometheus at the other hosts.

## Decision

- **`agent`** is a new stack: node-exporter + cAdvisor only, publishing `9100`
  and `8080` on the host's private VPC IP (and `127.0.0.1` for local
  debugging). No secrets, no storage, `min_ram_mb: 192`.
- **Implicit assignment**: an operator never lists `agent` in `deployment.yaml`.
  `workspace.Validate` (`normalizeImplicitStacks`) appends it to every host the
  moment any host carries `monitor`, before capacity/dependency checks run —
  so `mksrv validate`'s RAM accounting and `mksrv apply`'s dependency ordering
  both see it. This mirrors how `postgres`/`openbao` quorum size is inferred
  rather than declared per host.
- **`monitor`** keeps Prometheus + Grafana, drops node-exporter/cAdvisor
  (moved to `agent`), and depends on `agent`. `prometheus.yml` scrapes:
  - `node` / `cadvisor`: every `render.Context.Fleet` member's private IP —
    the new fleet-wide roster (all hosts, sorted by name, with `Role` and
    `Stacks`), a peer of the existing `StackHosts`/`StackMembers` accessors.
  - `patroni`: every `postgres` cluster member's `:8008/metrics` — Patroni
    serves Prometheus metrics natively, so this needs no exporter.
  - `loki` / `crowdsec`: unchanged, gated on presence.
- **Dashboards are provisioned, not manual**: a `dashboards/` volume + a file
  provisioner (`grafana-dashboards.yml`), with one bundled dashboard,
  `fleet-overview.json` — targets-up ratio, Patroni leader presence, and
  per-host CPU/memory/filesystem/load timeseries. The Prometheus and Loki
  datasources get fixed `uid`s (`prometheus`, `loki`) so bundled dashboard
  JSON can reference them deterministically.
- A `reload-prometheus.sh` post-deploy hook (`--web.enable-lifecycle` is
  already on the container) applies scrape-config changes without restarting
  Prometheus and losing the in-memory head block.
- **Deliberately deferred** (`--collector.systemd` on node-exporter): it needs
  the host's `/run/systemd/private` socket, fiddlier to wire through Podman
  than the value it adds in phase 1. Revisit when unit-failure alerting
  (`mksrv-backup.service` in particular) is implemented (phase 4).

## Consequences

- Zero Terraform changes. Zero new secrets.
- `mksrv apply`'s per-stack deploy log now shows an `agent` line on every host
  once any host in the fleet carries `monitor` — expected, not a bug.
- `+192 MiB` RAM per host. On the 3 `t4g.small` `postgres`/`openbao` cluster
  nodes this narrows headroom; `mksrv validate`'s `capacity.overcommit`
  warning (not an error) will flag it if it matters for the instance type.
- The `dev`/single-host and `examples/workspace` profiles gain `agent` too
  wherever `monitor` is present — harmless (it targets `cloud` and `local`
  alike) and gives the same fleet view even for one host.
- Deferred to later M23 phases: OpenBao/Postgres/Redis exporters, blackbox
  probing of public endpoints (TLS expiry), Keycloak/Caddy metrics, alert
  rules and notifications. See `docs/monitoring.md`.
