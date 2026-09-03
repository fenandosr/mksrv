# Tenant RBAC

Each tenant realm has four **additive** groups (ADR 0016). Put a user in any
combination in `tenants/<id>.users.yaml`:

```yaml
version: 1
tenant: acme
users:
  - email: lead@acme.example.com
    name: "Team Lead"
    groups: [admin]
  - email: dev1@acme.example.com
    groups: [dev]
  - email: analyst@acme.example.com
    groups: [apps, vpn]      # uses the app + reaches Jupyter, no build rights
```

`mksrv users apply <id>` reconciles membership.

## What each group grants

| | `admin` | `dev` | `apps` | `vpn` |
|---|:--:|:--:|:--:|:--:|
| App SSO (web) | ✅ | ✅ | ✅ | — |
| Cloud-IT VPN device | ✅ | ✅ | — | ✅ |
| Reach tenant mesh nodes (SSH/mosh to login nodes, internal APIs) | ✅ | ✅ | — | ✅¹ |
| Postgres `db_<id>` — DDL + read/write | ✅ | ✅ | — | — |
| Postgres `db_<id>` — read + granted writes | ✅ | ✅ | ✅² | — |
| OpenBao — read `kv/tenants/<id>/*`, write `kv/…/dev/*`, transit enc/dec | ✅ | ✅ | — | — |
| OpenBao — write all, version destroy, key rotate | ✅ | — | — | — |
| Manage the tenant's Keycloak users | ✅ | — | — | — |
| Change what's published (`forwards`, `dns`, mesh nodes, `stacks`) | ✅³ | — | — | — |

¹ subject to the tenant's Headscale ACL.
² `apps` requests land on the `<id>_app` Postgres role, which has `SELECT` on
`app` by default; the dev opens `INSERT`/`UPDATE`/`DELETE` per-table with `GRANT`
/ RLS. PostgREST picks the role from the token's `groups` claim via a
`db-pre-request` function (`admin`/`dev` → `<id>`, `apps` → `<id>_app`,
token-less → `<id>_anon`).
³ via a reviewed PR to `tenants/<id>.yaml` — the admin is the CODEOWNER; there is
no runtime "publish" API.

## admin vs dev

- **dev** builds *inside* the tenant's allocation: create schemas and tables,
  consume services, SSH to login nodes, read secrets, deploy app code.
- **admin** owns the tenant *boundary*: who is on the team and in which group,
  which physical nodes join the mesh, what gets a DNS name or a VPN forward,
  which stacks the tenant consumes, and custody of the tenant's secrets.

A dev cannot grant access to anyone (including themselves) or expose a new
service.

## Managing your team (admin)

An `admin` opens `https://auth.<operator-domain>/admin/<realm>/console`, adds
users, and assigns groups — no operator involvement. The admin cannot edit
clients, protocol mappers, or realm settings.

## Rollout status

- **M17** — groups, `groups` token claim, configd VPN gate, Keycloak team
  management. *Done.*
- **M18** — OpenBao per-group policies (`tenant-<id>-admin` / `-dev`) and OIDC
  roles bound on the `groups` claim. *Done.*
- **M19** — Postgres `<id>_app` role; PostgREST resolves the effective role from
  the token's `groups` claim (`db-pre-request` function). *Done.*

The RBAC model is fully enforced across Keycloak, the VPN, OpenBao, and Postgres.
