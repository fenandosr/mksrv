# Stack catalog

Each `stacks/<name>/stack.yaml` document is validated against
`schemas/stack.v1.json`. The descriptor declares target types, tenant scope,
dependencies, apps, networks, secret references, templates, hooks, resources,
and health checks.

| Stack | Target | Tenant scope | Dependencies | M0 state |
|---|---|---:|---|---|
| `base` | cloud | no | — | descriptor |
| `identity` | cloud | yes | base | descriptor |
| `mail` | cloud | no | base | descriptor |
| `database` | cloud/local | yes | identity | descriptor |
| `files` | cloud/local | yes | identity | descriptor |
| `analytics` | cloud/local | yes | database, identity | descriptor |
| `monitor` | cloud/local | yes | identity | descriptor |

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
