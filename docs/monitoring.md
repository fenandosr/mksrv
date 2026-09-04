# Monitoring

`monitor` (Prometheus + Grafana) and `agent` (node-exporter + cAdvisor,
auto-assigned to every host — ADR 0019) give the fleet a central metrics view.
This page tracks what is scraped today, what dashboards ship, and what M23's
later phases add.

## What's scraped (M23 phase 1)

| Job | Source | Every host? |
|---|---|---|
| `prometheus` | Prometheus itself | n/a |
| `node` | `agent`'s node-exporter, `<host private ip>:9100` | yes |
| `cadvisor` | `agent`'s cAdvisor, `<host private ip>:8080` | yes |
| `patroni` | Patroni's native `/metrics`, `<postgres node ip>:8008` | postgres cluster nodes only |
| `loki` | when the `logs` stack is present | no |
| `crowdsec` | when the `security` stack is present | no |

No Terraform change was needed: `infra/modules/aws-host` already allows all
TCP between fleet hosts inside the VPC (`intra_vpc` security group rule).

## Dashboards

Bundled and auto-provisioned (Grafana → Dashboards → mksrv):

- **Fleet overview** (`mksrv-fleet-overview`) — targets-up ratio, Patroni
  leader presence, hosts/containers reporting, and per-host CPU / memory /
  filesystem / load timeseries.

Import these from grafana.com for deeper per-subsystem views (they are not
vendored, to avoid license drift):

| Dashboard | ID | Once available |
|---|---|---|
| Node Exporter Full | `1860` | now (`node` job) |
| cAdvisor exporter | `19792` | now (`cadvisor` job) |
| Prometheus | `19105` | now |
| Patroni | see the [Patroni repo](https://github.com/zalando/patroni)'s `extras/grafana/` | now (`patroni` job) |
| PostgreSQL Database | `9628` / `14114` | after `postgres_exporter` (phase 2) |
| Redis Dashboard | `763` | after `redis_exporter` (phase 2) |
| Vault | `12904` | after OpenBao telemetry (phase 2) |
| Prometheus Blackbox Exporter | `7587` | after `blackbox_exporter` (phase 3) |
| Keycloak Metrics | `10441` (or the KC 25+ equivalent) | after Keycloak metrics (phase 3) |
| Caddy | `13859` | after the Caddy metrics vhost (phase 3) |

## Credentials

- Grafana admin: `admin` / SSM `/mksrv/{env}/monitor/grafana_admin_password`.
- Prometheus and Loki datasources are provisioned with fixed UIDs (`prometheus`,
  `loki`) so bundled dashboard JSON can reference them without a lookup.

## Roadmap (M23, phases 2–4)

- **Phase 2** — OpenBao telemetry (`sys/metrics`), `postgres_exporter` per
  Patroni node, `redis_exporter` on `cache`; their dashboards.
- **Phase 3** — `blackbox_exporter` probing every operator FQDN (HTTP 200,
  TLS-expiry countdown), Keycloak's built-in metrics (`KC_METRICS_ENABLED`),
  a Caddy metrics vhost.
- **Phase 4** — a `mksrv_backup_last_success_seconds` textfile metric, an
  alert-rule catalog (host resource pressure, `PatroniNoLeader`,
  `OpenBaoSealed`, `CertExpiringSoon`, `BackupStale`, …), and Grafana-managed
  notifications (webhook or SMTP contact point).

See ADR 0019 for the design rationale.
