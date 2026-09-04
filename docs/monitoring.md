# Monitoring

`monitor` (Prometheus + Grafana) and `agent` (node-exporter + cAdvisor,
auto-assigned to every host — ADR 0019) give the fleet a central metrics view.
This page tracks what is scraped today, what dashboards ship, and what M23's
later phases add.

## What's scraped (M23 phases 1–2)

| Job | Source | Every host? |
|---|---|---|
| `prometheus` | Prometheus itself | n/a |
| `node` | `agent`'s node-exporter, `<host private ip>:9100` | yes |
| `cadvisor` | `agent`'s cAdvisor, `<host private ip>:8080` | yes |
| `patroni` | Patroni's native `/metrics`, `<postgres node ip>:8008` | postgres cluster nodes only |
| `postgres-exporter` | co-located with Patroni, `<postgres node ip>:9187` | postgres cluster nodes only |
| `openbao` | OpenBao's native telemetry, `<openbao node ip>:8200/v1/sys/metrics` | openbao cluster nodes only |
| `redis-exporter` | co-located with `cache`, `<cache host ip>:9121` | no (one `cache` host) |
| `keycloak` | Keycloak's built-in metrics, `<identity host ip>:9000` | no (one `identity` host) |
| `caddy` | proxied through a private-IP-only vhost, `<base host ip>:9019` | no (one `base` host) |
| `blackbox` | every operator + tenant-rest FQDN, probed over HTTPS | n/a (external targets) |
| `loki` | when the `logs` stack is present | no |
| `crowdsec` | when the `security` stack is present | no |

No Terraform change was needed: `infra/modules/aws-host` already allows all
TCP between fleet hosts inside the VPC (`intra_vpc` security group rule).

The `postgres-exporter` and `redis-exporter` sidecars run with `Network=host`
and connect over `127.0.0.1` — the same loopback address the owning service
already publishes for local debugging — rather than the stack's podman bridge
network, so nothing needs adding to a *live* Patroni cluster's `pg_hba` (it is
only applied at `initdb` time) or Redis's ACL rules.

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
| PostgreSQL Database | `9628` / `14114` | now (`postgres-exporter` job) |
| Redis Dashboard | `763` | now (`redis-exporter` job) |
| Vault | `12904` | now (`openbao` job) |
| Prometheus Blackbox Exporter | `7587` | now (`blackbox` job) |
| Keycloak Metrics | `10441` (or the KC 25+ equivalent) | now (`keycloak` job) |
| Caddy | `13859` | now (`caddy` job) |

## Credentials

- Grafana admin: `admin` / SSM `/mksrv/{env}/monitor/grafana_admin_password`.
- Prometheus and Loki datasources are provisioned with fixed UIDs (`prometheus`,
  `loki`) so bundled dashboard JSON can reference them without a lookup.

## Roadmap (M23, phase 4)

- **Phase 4** — a `mksrv_backup_last_success_seconds` textfile metric, an
  alert-rule catalog (host resource pressure, `PatroniNoLeader`,
  `OpenBaoSealed`, `CertExpiringSoon`, `BackupStale`, `EndpointDown`, …), and
  Grafana-managed notifications (webhook or SMTP contact point).

See ADR 0019 (phase 1), ADR 0020 (phase 2), and ADR 0021 (phase 3) for the
design rationale.
