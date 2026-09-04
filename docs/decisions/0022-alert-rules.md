# ADR 0022: alert-rule catalog, no auto-provisioned notifications

- Status: Accepted
- Date: 2026-09-04
- Milestone: M23 (phase 4 of 4)

## Context

Phases 1–3 gave the fleet metrics coverage from the host up through the
public edge. None of it was actionable yet — nobody is paged, and nothing
distinguishes "everything is fine" from "something is on fire" without
someone opening Grafana.

Two things were needed: a way to know a *backup* succeeded (nothing measured
it), and a rule catalog that turns the now-rich metric surface into a small
number of binary "is this OK" signals.

## Decision

- **Backup freshness**: `backup.sh` writes
  `mksrv_backup_last_success_seconds` to `agent`'s node-exporter textfile
  directory (`/var/lib/mksrv/metrics`, wired since phase 1) on a successful
  run — atomically (temp file + `mv`), so the collector never reads a
  half-written file.
- **Alert rules** (`stacks/monitor/templates/prometheus-rules.yml.tmpl`,
  loaded via Prometheus's `rule_files:`): four groups —
  `mksrv-fleet` (`TargetDown`, `HostDiskAlmostFull`, `HostLowMemory`,
  `HostHighCPU`), `mksrv-data-plane` (`PatroniNoLeader`, `OpenBaoSealed`,
  `RedisMemoryHigh`, `PostgresConnectionsSaturated`), `mksrv-edge`
  (`EndpointDown`, `CertExpiringSoon`), `mksrv-backup` (`BackupStale`, > 26h
  since the last success — the daily timer plus an hour of jitter, plus
  headroom). Every expression only references metrics from phases 1–3; a rule
  for a subsystem that isn't deployed (no `cache`, no `openbao`, …) simply
  never fires — Prometheus does not error on an alert expression that
  currently has no data.
- **A Go-template escaping wrinkle**: rule `annotations` use Prometheus's own
  `{{ $labels.host }}`-style templating, expanded by Prometheus when an alert
  *fires* — but this file is *also* a template mksrv's `render` package
  parses with Go's `text/template`. Each such placeholder is wrapped
  `{{`{{ $labels.host }}`}}` (a Go action whose body is a raw string
  literal) so mksrv's renderer emits it unchanged instead of trying to
  resolve `$labels` as one of its own context fields. Verified by rendering
  the template standalone and parsing the result as YAML.
- **A "Firing alerts" stat panel** (`sum(ALERTS{alertstate="firing"}) or
  vector(0)`) joins `fleet-overview`'s top row — the one number that answers
  "is anything wrong" without reading the rule list.
- **Deliberately not shipped: auto-provisioned notifications.** Grafana's
  file-provisioned alerting (contact points + notification policies) has a
  real schema, but nothing in this session could exercise it against a live
  Grafana instance, and this system is already running production traffic.
  Shipping unverified provisioning YAML risks Grafana logging errors on every
  restart or, worse, alerting silently not routing anywhere while looking
  configured. `docs/monitoring.md` instead documents the two-minute manual
  path (Alerting → Contact points → add a receiver; Notification policies →
  point the default policy at it) — a deliberate, disclosed scope cut, not an
  oversight.

## Consequences

- No Terraform change, no new secrets, no measurable RAM increase (the rules
  file is static, no per-target cost).
- Alerts are visible in Prometheus's own `/alerts` and queryable via `ALERTS`
  in Grafana, but nothing pages anyone until a contact point is wired up
  manually (see `docs/monitoring.md`).
- This closes M23. Any further monitoring work (notifications, more
  subsystem-specific rules, Alertmanager for anything Grafana-managed
  alerting doesn't cover) is a new milestone, not a phase of this one.
