# ADR 0020: per-service exporters (OpenBao, Postgres, Redis)

- Status: Accepted
- Date: 2026-09-04
- Milestone: M23 (phase 2 of 4)

## Context

ADR 0019 gave the fleet host- and container-level metrics. The stateful
services themselves — Patroni's quorum status aside, already free — were
still opaque: no query/connection/replication-lag detail for Postgres, no
seal/lease/HA-leader detail for OpenBao, no hit-rate/eviction detail for
Redis.

Two questions had to be settled per exporter: where does it run, and how does
it authenticate.

## Decision

- **OpenBao**: no separate exporter. OpenBao/Vault serve their own Prometheus
  telemetry. `openbao.hcl` gains a `telemetry` stanza
  (`prometheus_retention_time`, `disable_hostname`) and the listener's
  `telemetry { unauthenticated_metrics_access = true }`. Unauthenticated
  `/v1/sys/metrics` is acceptable on the same intra-VPC boundary that already
  makes the rest of the listener plaintext (ADR 0005) — Prometheus has no
  other practical way to hold a scoped metrics token, and the alternative
  (minting and rotating one, as `backup`'s raft-snapshot token does) is a lot
  of plumbing for a read-only, non-sensitive metrics endpoint.
- **Postgres**: `postgres_exporter` runs as a sidecar app *inside* the
  `postgres` stack, one per Patroni node, `Network=host`, connecting over
  `DATA_SOURCE_URI=127.0.0.1:5432/postgres?sslmode=disable` with
  `DATA_SOURCE_USER`/`DATA_SOURCE_PASS` (the latter from the existing
  `superpass` podman secret — no new secret).
- **Redis**: `redis_exporter` runs the same way inside `cache`, `Network=host`,
  `REDIS_ADDR=redis://127.0.0.1:6379`, reusing the existing
  `redis_admin_pass` secret and the `mksrv` ACL user.
- **Why `Network=host` and not the stack's podman bridge network**: Patroni's
  `pg_hba` allows `10.20.0.0/16` (the VPC CIDR) and `127.0.0.1/32` — not the
  podman bridge's subnet — and it is written only at `bootstrap`/`initdb`
  time, so it cannot be extended on an already-bootstrapped, live cluster
  without an out-of-band `patronictl edit-config`. Connecting over
  `127.0.0.1`, the address the service already publishes there for local
  debugging, needed no cluster change at all — important since this landed
  against an already-running production Patroni cluster. Redis has no
  equivalent obstacle but uses the same shape for consistency.
- `monitor`'s `prometheus.yml` gains three more gated jobs: `postgres-exporter`
  and `openbao` over `.StackNodes(...)`, `redis-exporter` over `.StackIP("cache")`.

## Consequences

- No new secrets, no Terraform change.
- `+~30 MiB` RAM per Patroni node (`postgres` stack `min_ram_mb` 1024→1088),
  `+~32 MiB` on the `cache` host (384→416).
- OpenBao's metrics endpoint is reachable, unauthenticated, by anything inside
  the VPC — narrower than "the whole internet" (the listener itself is
  already VPC-only) but broader than "only Prometheus". Revisit if OpenBao
  ever gets a public listener.
- Dashboards: Vault (`12904`), PostgreSQL Database (`9628`/`14114`), Redis
  Dashboard (`763`) — import IDs documented in `docs/monitoring.md`, not
  vendored (license drift).
- Deferred to phases 3–4: blackbox probing, Keycloak/Caddy metrics, alert
  rules and notifications.
