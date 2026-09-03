# ADR 0016: Per-tenant RBAC groups (admin / dev / apps / vpn)

- Status: Accepted
- Date: 2026-09-03
- Milestone: M17 (+ M18, M19)

## Context

Every subsystem in mksrv is scoped **per tenant**, never per person. The realm
groups `apps` / `both` existed but nothing read them. There was no way to say
"this person builds, that one just uses the app, that one only needs the VPN,
and this one runs the team." A tenant admin could not manage their own users —
that was operator-only via `*.users.yaml`.

## Decision

Four additive groups per tenant realm. A user is in any combination.

| Group | Keycloak | VPN (configd) | Mesh | Postgres | OpenBao | Apps SSO | Publish |
|---|---|---|---|---|---|---|---|
| `admin` | realm group + team-management roles | ✅ | tenant nodes | `<id>` (RW + DDL in `db_<id>`) | RW `kv/tenants/<id>/*` + transit | ✅ | ✅ (git/PR owner) |
| `dev` | realm group | ✅ | tenant nodes | `<id>` (RW + DDL) | R `kv/*`, RW `kv/dev/*`, transit enc/dec | ✅ | ❌ |
| `apps` | realm group | ❌ | — | `<id>_app` (SELECT by default; per-table grants) | — | ✅ | ❌ |
| `vpn` | realm group | ✅ | tenant ACL | — | — | ❌ | ❌ |

- **admin vs dev** is the tenant *boundary* vs building *inside* it:
  - admin manages the team (Keycloak fine-grained `manage-users` / `query-users`
    / `query-groups` / `view-users` on the `admin` group — *not* `manage-clients`
    or `manage-realm`, so protocol mappers and client config stay operator-only),
  - admin owns `tenants/<id>.yaml` (mesh enrolment, `forwards`, `dns`,
    `mesh_routes`, `stacks`) — enforced at the workspace repo (CODEOWNERS), since
    "publishing" is a deploy-time operator action, not a runtime API,
  - admin is the OpenBao secrets custodian.
- **dev** gets full DDL inside `db_<id>` (schemas, tables, extensions) — not
  separate databases (cluster-level). `apps` write access is per-table, granted
  by the dev (`GRANT` / RLS in pgAdmin), not a global switch.
- **vpn** is a standalone capability: ops or a data scientist who only needs to
  reach Jupyter over the mesh, without dev rights. `dev` / `admin` already imply
  VPN, so nobody needs `dev` + `vpn`.

## Mechanism

- `mksrv tenant apply` creates the four groups, adds an
  `oidc-group-membership-mapper` (`groups` claim, flat names) to the
  `cloud-it-vpn-desktop` and `openbao` clients, and grants the team-management
  roles to the `admin` group (`RealmSpec.AdminGroup`, `ClientSpec.GroupsClaim`).
- **configd** (M17): `Claims.Groups`; `/v1/clientconfig` returns 403 unless the
  token carries one of `{vpn, dev, admin}`. Image rebuilt by CI on merge.
- **OpenBao** (M18): per-group OIDC roles + policies bound on the `groups` claim,
  replacing the single `tenant-<id>` role.
- **Postgres / PostgREST** (M19): roles `<id>` (dev/admin), `<id>_app` (SELECT
  default + dev-granted writes), `<id>_anon` (token-less), and `<id>_web` — the
  claim PostgREST impersonates (`role: <id>_web`, one hardcoded mapper, no
  conditionals in Keycloak). `<id>_web` is `NOINHERIT` and a member of the other
  three; a `db-pre-request` plpgsql function reads `request.jwt.claims->groups`
  and `SET LOCAL ROLE`s to `<id>` / `<id>_app` / `<id>_anon`. The group→role
  logic lives in SQL, not Keycloak.

## Consequences

- `*.users.yaml`: `both` → `admin` for the existing tenant-admin seed users.
- On an **already-provisioned** realm the `mksrv-role` hardcoded-claim mapper
  keeps its old `<id>` value (`applyClaimMappers` is create-if-absent) — delete
  it once so `tenant apply` recreates it as `<id>_web`. A fresh install is
  unaffected.
- Deferred: a self-service `mksrv publish` command; Grafana OIDC role mapping;
  group→policy scoping finer than the four groups.
