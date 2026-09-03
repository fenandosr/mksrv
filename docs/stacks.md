# Stack catalog

Each `stacks/<name>/stack.yaml` document is validated against
`schemas/stack.v1.json`. The descriptor declares target types, tenant scope,
dependencies, apps, networks, secret references, templates, hooks, resources,
and health checks.

A stack's `kind` is `service` (one instance per assigned host, the default) or
`cluster` (an odd number of hosts ≥ 3, self-organising quorum — see ADR 0013).
A `storage:` block requests dedicated EBS volumes mounted at
`/var/lib/mksrv/vol/<name>` (ADR 0014).

| Stack | Kind | Target | Tenant scope | Dependencies | State |
|---|---|---|---:|---|---|
| `base` | service | cloud | no | — | implemented |
| `identity` | service | cloud | yes | base | implemented |
| `mail` | service | cloud | no | base | descriptor |
| `postgres` | cluster | cloud | no | — | implemented (opt-in) |
| `openbao` | cluster | cloud | yes | identity | implemented (opt-in) |
| `database` | service | cloud/local | yes | identity (+ `postgres` cluster when present) | implemented |
| `cache` | service | cloud/local | yes | identity | implemented |
| `files` | service | cloud/local | yes | identity | descriptor |
| `analytics` | service | cloud/local | yes | database, identity | descriptor |
| `monitor` | service | cloud/local | yes | identity | implemented |
| `logs` | service | cloud/local | yes | monitor | implemented (opt-in) |
| `security` | service | cloud/local | no | logs, monitor | implemented (opt-in) |

Templates and hooks are intentionally empty in M0. Their implementation belongs
to M2–M5 and must be golden-tested.

## Derived tenant endpoints

For tenant `base_domain: acme.example.com`, stack endpoints use:

- `db.acme.example.com`
- `pgadmin.acme.example.com`
- `files.acme.example.com`
- `analytics.acme.example.com`
- `grafana.acme.example.com`

Realm login is served below the deployment Keycloak domain at
`/realms/<realm>`. Mesh node naming is derived from the tenant ID and the
Headscale/tailnet configuration.

## Quadlet conventions for implementation milestones

Units use `Network=mksrv-<stack>`, stable `mksrv-...` container names,
`Restart=always`, and `WantedBy=multi-user.target`. Named volumes are preferred;
bind mounts use `:Z` for SELinux relabeling. Postgres must never be exposed to
`0.0.0.0/0`; data-plane access is through the mesh.
