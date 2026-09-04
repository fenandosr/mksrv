# ADR 0021: edge visibility — blackbox probing, Keycloak, Caddy metrics

- Status: Accepted
- Date: 2026-09-04
- Milestone: M23 (phase 3 of 4)

## Context

Phases 1–2 gave the fleet host, container, and data-plane metrics, but the
public edge — the part users actually hit — was still unmeasured: nobody
watched TLS certificate expiry, whether the operator endpoints actually
resolved and answered, Keycloak's own login/session/JVM health, or Caddy's
request rate and latency.

## Decision

- **blackbox_exporter** joins the `monitor` stack (`mksrv-monitor.network`,
  reached by Prometheus over container DNS — no host port needed beyond a
  `127.0.0.1` publish for the deploy health check). One module, `http_2xx`
  (10s timeout, follows redirects — Grafana/pgAdmin's 302-to-login still
  counts as healthy).
- **What it probes**: `render.Context.OperatorFQDNs`, a new field the CLI
  populates from the same inputs as `infra/root/main.tf`'s
  `local.operator_fqdns` — the 5 shared operator domains plus one
  `<id>.rest.<root>` per tenant that carries `database`. Kept in Go rather
  than read from Terraform state so the render layer doesn't need a
  Terraform round-trip; the two lists must be kept in sync by inspection
  (both are short and change rarely).
  `probe_ssl_earliest_cert_expiry` on every one of these targets is the
  cheapest way to know a Let's Encrypt renewal is failing *before* it expires.
- **Keycloak**: metrics were already enabled (`KC_METRICS_ENABLED=true`,
  `KC_HEALTH_ENABLED=true`, management port `9000`) since the `identity`
  stack's first implementation — just never published beyond `127.0.0.1`.
  Publishing it on the private IP too was the entire change.
- **Caddy**: metrics are **not** exposed by publishing the admin API
  (`127.0.0.1:2019`) on the private IP — that API can reload config and stop
  the process, and Caddy is the fleet's only public-facing host. Instead a
  `servers { metrics }` global option turns on per-server metrics on the
  admin listener, and a new vhost on the private IP,
  `{{ .Host.PrivateIP }}:9019`, proxies only `/metrics` through to
  `127.0.0.1:2019`; every other path 404s without ever reaching the admin
  API.

## Consequences

- No Terraform change, no new secrets.
- `+32 MiB` RAM on the `monitor` host for blackbox (`min_ram_mb` 1024→1056).
- `render.Context` gains `OperatorFQDNs []string`; a future stack that needs
  "every public endpoint" (e.g. a status page) can reuse it instead of
  re-deriving the list a third time.
- Dashboards: Blackbox Exporter (`7587`), Keycloak Metrics (`10441` or the
  KC 25+ equivalent), Caddy (`13859`) — import IDs in `docs/monitoring.md`.
- Deferred to phase 4: an alert-rule catalog that actually uses
  `probe_ssl_earliest_cert_expiry` / `probe_success` (`CertExpiringSoon`,
  `EndpointDown`), plus notifications.
