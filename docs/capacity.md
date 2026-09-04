# Capacity planning

mksrv does not schedule workloads — you assign stacks to hosts in
`deployment.yaml` and size each host yourself. Two aids exist.

## RAM overcommit warning

Each `stacks/<name>/stack.yaml` declares a conservative `resources.min_ram_mb`.
On `mksrv validate`, for every `provider: aws` host, mksrv sums the `min_ram_mb`
of its assigned stacks and compares it to the instance memory
(`internal/model.InstanceRAMMB`, a table of the t/m/c/r families). If the sum
exceeds instance RAM it emits a **`capacity.overcommit` warning** — the
workspace stays valid; it is a prompt to pick a larger `instance_type` or split
the stacks across hosts. Unknown instance types are skipped silently.

## Adaptive swap

Swap is managed by the bootstrap script (re-run on every `mksrv apply`), not by
first-boot Terraform. Per host:

```
swap = clamp( sum(min_ram_mb) + 512 headroom − instance_RAM,  0,  min(instance_RAM, 4096) )   # rounded up to 512 MiB
```

An unknown instance type falls back to the legacy rule: 2048 MiB when the host
carries `identity`, else 0. Changing a host's stack set or `instance_type`
re-sizes the swapfile on the next `apply`.

## Sizing the distributed profile

Rough per-service memory (idle → light load):

| Service | RAM | Notes |
|---|---|---|
| PostgreSQL (Patroni) | 250 → 800 MiB | `shared_buffers 256MB`; fsync-sensitive disk |
| OpenBao | 40 → 512 MiB | Raft BoltDB, low write rate |
| Keycloak (JVM) | 0.8 → 1.2 GiB | DB on the cluster |
| Prometheus | 300 MiB → scales with series | TSDB, retention-sized disk |
| Loki | 250 → 400 MiB | chunks on filesystem or S3 |
| Grafana / Headscale / configd | 150 / 60 / 15 MiB | |
| `agent` (node-exporter + cAdvisor, every host) | 192 MiB | auto-assigned when `monitor` is present (ADR 0019) |
| `postgres_exporter` / `redis_exporter` (M23 phase 2) | 30 / 32 MiB | co-located sidecars in `postgres` / `cache` |
| Alloy · CrowdSec · Tailscale (per host) | 80 · 150 · 30 MiB | |
| Caddy | 30 MiB | |

Instance memory (`InstanceRAMMB`):

| Type | MiB | | Type | MiB |
|---|---:|---|---|---:|
| `t4g.nano` | 512 | | `t4g.medium` / `m7g.medium` | 4096 |
| `t4g.micro` | 1024 | | `t4g.large` / `m7g.large` | 8192 |
| `t4g.small` | 2048 | | `c7g.large` | 4096 |
| `t4g.xlarge` | 16384 | | `r7g.large` | 16384 |

A "minimum good dev" distributed fleet (~5 concurrent, 40 users): 3× `t4g.small`
core (Patroni + Bao + agents) + 2× `t4g.medium` app (Keycloak + Caddy +
Prometheus + Grafana + Loki). ~$70–100/mo on-demand, ~$50/mo with a 1-year
Savings Plan.

Both `postgres` and `openbao` are `kind: cluster` (odd ≥ 3) and are meant to
co-reside on the same 3 core hosts — one Patroni node and one OpenBao node per
host. OpenBao adds ~40–120 MiB idle and a small `baoraft` gp3 volume; it does
not change the instance sizing above.
