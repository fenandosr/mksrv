# Monitoring

`monitor` (Prometheus + Grafana) and `agent` (node-exporter + cAdvisor,
auto-assigned to every host — ADR 0019) give the fleet a central metrics view.
This page tracks what is scraped today, what dashboards ship, and what M23's
later phases add.

## What's scraped

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

## Alert rules

`stacks/monitor/templates/prometheus-rules.yml.tmpl`, loaded via Prometheus's
`rule_files:`. A rule for a subsystem that isn't deployed simply never fires
(no error). Visible in Prometheus's own `/alerts`, and as a count in
Grafana's `fleet-overview` dashboard (`sum(ALERTS{alertstate="firing"})`).

| Alert | Group | Fires when | For |
|---|---|---|---|
| `TargetDown` | fleet | a scrape target has been unreachable | 5m |
| `HostDiskAlmostFull` | fleet | a filesystem has < 12% free | 10m |
| `HostLowMemory` | fleet | a host has < 10% memory available | 10m |
| `HostHighCPU` | fleet | a host's CPU usage is above 90% | 15m |
| `PatroniNoLeader` | data-plane | no Patroni node reports itself as leader | 2m |
| `OpenBaoSealed` | data-plane | an OpenBao node is sealed | 1m |
| `RedisMemoryHigh` | data-plane | Redis is above 85% of `maxmemory` | 10m |
| `PostgresConnectionsSaturated` | data-plane | above 80% of `max_connections` | 10m |
| `EndpointDown` | edge | a blackbox probe (an operator/tenant FQDN) fails | 5m |
| `CertExpiringSoon` | edge | a TLS cert has < 14 days left | 1h |
| `BackupStale` | backup | no successful `mksrv-backup` run in > 26h | — |

## Wiring up notifications (manual, ~2 minutes)

Rules fire and show up in Grafana/Prometheus regardless, but nothing pages
anyone until a contact point exists. This is deliberately **not**
auto-provisioned (ADR 0022) — Grafana's alerting provisioning YAML has real
schema risk that this session had no live instance to verify against, and
this system runs production traffic. Do it once, in the UI:

1. Grafana → **Alerting → Contact points** → **Add contact point** — pick
   `Slack`, `Webhook`, or `Email` and fill in the destination.
2. **Alerting → Notification policies** → edit the default policy → set its
   contact point to the one just created.
3. **Alerting → Alert rules** → **New alert rule**, query the Prometheus
   datasource with `ALERTS{alertstate="firing"}` (or import individual rules
   from `prometheus-rules.yml` one at a time) so Grafana evaluates and routes
   them — Grafana's own alerting engine needs its own rule copy; it does not
   read Prometheus's `rule_files:` automatically.

See ADR 0019 (phase 1), ADR 0020 (phase 2), ADR 0021 (phase 3), and ADR 0022
(phase 4) for the full design rationale. M23 is complete as of phase 4.
