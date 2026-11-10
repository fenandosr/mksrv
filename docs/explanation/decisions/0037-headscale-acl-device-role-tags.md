# ADR 0037 (draft): Headscale ACLs by device role tag, not just by tenant

- Status: Proposed — open questions below, no decision yet
- Date: 2026-11-09
- Refines: ADR 0011 (tenant-owned infrastructure), ADR 0016 (tenant RBAC
  groups), ADR 0028 (tenant web endpoints), ADR 0034 (tenant local hosts)

## Context

`headscale.Policy()` (`internal/headscale/headscale.go:218`) grants per
**Headscale user**, and every device a tenant registers — a human's laptop, a
`local_hosts` meshnode instance, an operator-administered host serving that
tenant — lands under the same one user (e.g. `hg`). Confirmed live against
the actual loaded policy (`headscale policy get` on the edge), not just the
code: `{"action":"accept","src":["hg@"],"dst":["hg@:*"]}` is exactly what's
running today, for every tenant.

Three consequences, found working through a real case (a tenant developer
getting SSH to an operator-administered host over the mesh):

1. **The RBAC groups (`dev`/`admin`/`vpn`, ADR 0016,
   `docs/reference/rbac.md`) don't exist at the network layer.** A person who
   is only in `vpn` reaches exactly what an `admin` reaches over the mesh —
   the ACL has no way to express that distinction, only "this tenant's
   devices." Any doc note that reads "subject to the tenant's Headscale ACL"
   as if that were a group-aware filter is promising something the ACL
   cannot deliver.
2. **`<id>@ → <id>@:*` gives any device of a tenant every port of every one
   of that tenant's nodes.** On a real host (`ome`, serving tenant `hg`),
   firewalld narrows this locally to a couple of ports — but one of those
   ports is the tenant's own production `gunicorn` (`:8000`): reachable
   directly over the mesh, with no edge, no TLS, no SSO in front of it.
   Inside a `meshnode` instance's own network namespace there is no firewall
   at all — anything listening there is exposed to the tenant's whole mesh.
3. **Service nodes (`meshnode` instances) are registered under the same
   tenant user as human devices**, so they can *also* reach everything that
   user reaches — other devices of the tenant, and (via the existing
   `<id>@ → mksrv-fleet@:<fleetPorts>` rule) the fleet's own Postgres,
   OpenBao, Redis. A compromised app container inherits all of that reach,
   not just the one database connection it actually needs.

The motivating case: a tenant's developer gets SSH to an operator-shared host
over the mesh, as a real Tailscale client registered under the tenant's
Headscale user (the Cloud-IT VPN app — forward-only — doesn't work for this;
real Tailscale is required). `sshd` itself still gates *that* access
correctly (she only reaches her own sidecar over SSH). The gap is what the
mesh membership *also* grants her once she's on it: direct reach to
production `:8000` and to whatever else listens inside any of that tenant's
`meshnode` instances.

## Proposed direction (not implemented)

Pre-auth keys minted with a **tag** for the registering device's role,
operator-controlled (`tagOwners` — a tenant cannot self-assign a tag):

| tag | for |
|---|---|
| `tag:<id>-svc` | service nodes — `meshnode` instances (`mesh-hg-web`, `mesh-hg-stg`, …) |
| `tag:<id>-dev` | human devices for `dev`/`admin` group members |
| `tag:<id>-vpn` | human devices for `vpn`-only members — scoped to that tenant's `forwards:` targets, nothing wider |
| `tag:<id>-host` | operator-administered hosts serving that tenant (e.g. `ome`) |

`configd` (`internal/configd/server.go`) already holds the person's RBAC
group claims the moment it mints their pre-auth key — the fix is for *it* to
mint with the tag matching that group (`tag:<id>-vpn` for `vpn`-only,
`tag:<id>-dev` for `dev`/`admin`), not a separate per-tenant-user key handed
out identically regardless of group. `mksrv mesh` / `mksrv tenant mesh` mint
the `-svc`/`-host` tags for everything that isn't a person.

Rules by tag, replacing the blanket `<id>@ → <id>@:*`. **Which rule a given
endpoint needs is derived from where it actually lives (a host, or a
`meshnode` instance) — not assumed to always be `svc`.** `hg`'s own
production today is the host-targeted case: its `web:` points at the host's
own Tailscale IP (`100.64.0.11:8000`, not a meshnode), and its compose stack
reaches Postgres/Redis/OpenBao through that same host identity — so a
tag migration has to carry both the `-svc` and `-host` variants of the
edge/data-plane rules, or it breaks exactly that:

```
tag:<id>-dev  → tag:<id>-host:22                        # SSH to the host; sshd decides who
tag:<id>-dev  → mksrv-fleet@:<fleetPorts>                # what RBAC claims to give dev today
tag:<id>-vpn  → <that tenant's forwards: targets>        # nothing wider than today's forwards -- see the caveat below
mksrv-fleet@  → tag:<id>-svc:<WebPorts>                  # edge → a meshnode-hosted web: origin (ADR 0028)
mksrv-fleet@  → tag:<id>-host:<WebPorts>                 # edge → a host-hosted web: origin (hg's actual production shape today)
tag:<id>-svc  → mksrv-fleet@:5432,6379,8200              # a meshnode-hosted app to its own data services
tag:<id>-host → mksrv-fleet@:5432,6379,8200              # a host-hosted app to its own data services (hg's compose, today)
```

Deliberately **no** rule for: `dev → svc`, `svc → host:22`, `svc → svc`
between different instances, `dev → dev`, `vpn → vpn` (two laptops of the
same tenant have no reason to see each other), or `vpn → host` (a `vpn`
device reaches only its `forwards:` targets, never the host directly — the
host-reaching rules above are `dev`-only). `<id>@ → <id>@:*` would stay only
for untagged devices (backward compatibility during migration) or be retired
outright once every device of a tenant carries a tag.

**Caveat on `tag:<id>-vpn → <forwards: targets>`**: `forwards:` is declared
per *tenant*, not per RBAC group — if a tenant's `forwards:` list includes a
target that happens to be Postgres or OpenBao (some do, e.g. a `database` or
`openbao` forward), a `vpn`-only member reaches that port too, same as
`dev`/`admin` do today; only the service's own authentication (the Postgres
password, the OpenBao token) gates it from there, not this ACL. This is a
real limitation of this proposal, not something it fixes — closing it would
need `forwards:` to carry its own per-group scoping, which doesn't exist
today and is out of scope here. State it; don't pretend `tag:<id>-vpn` make
every forward `vpn`-safe by itself.

**A host serving more than one tenant needs one Tailscale identity per
tenant, never one `tag:<id>-host` identity shared across tenants** — sharing
would merge their data-plane reach into the same node. `ome` already has
leftover evidence this was anticipated and never finished: a firewalld zone
`mksrv-bitabit` (interface `ts-bitabit`, SSH only) exists on it today with no
matching interface — a second tenant's host identity was planned for that
box and never actually stood up. The `-host` tag model above assumes one
tagged node per (host, tenant) pair, the same shape `meshnode`'s
`multi_instance` already uses for service nodes.

## Open questions

1. **`vpn` vs `dev` — settled direction, not actually open.** `configd`
   already has the person's group claims at the exact moment it mints their
   pre-auth key (`claims.VPNEntitled()` already distinguishes the groups
   today, it just doesn't act on *which* one). Have it mint `tag:<id>-vpn`
   for a `vpn`-only member and `tag:<id>-dev` for `dev`/`admin` — this
   enforces the RBAC groups at the network layer with no Headscale OIDC
   involved. OIDC against the tenant's Keycloak realm stays on the table only
   as a *separate* feature, for when per-person identity on the mesh is
   wanted for its own sake (audit trail: which specific person's device did
   X) — not as a prerequisite for group-based filtering, which tags already
   solve.
2. **Does a tag replace the user match, in *this* Headscale version, for
   `src`/`dst`?** Confirmed what's actually deployed: `ghcr.io/juanfont/
   headscale:0.26.0`, pinned in `stacks/identity/stack.yaml`, not overridden
   anywhere in `deployment.yaml` — this is the exact version to test against,
   not just the template default. On Tailscale SaaS, a tagged device's
   identity is the tag set, not the user who registered it —
   `autogroup:self`/`autogroup:member`-style user rules stop matching it.
   General Tailscale ACL documentation and community reports describe the
   same convention, but nothing found confirms it specifically for Headscale
   0.26.0 — **not verified, only researched**. This matters directly for the
   migration question below: if a tag *does* replace the user match here,
   the `<id>@ → <id>@:*` fallback does **not** cover a device the moment it
   gets tagged, and a partial migration (some devices tagged, some not)
   silently cuts off the tagged ones instead of leaving them on the old
   blanket rule. Needs an actual test, against 0.26.0 specifically, on a
   disposable test node — not production — confirming with `headscale policy
   get` / live connectivity whether `<id>@` rules still reach a tagged
   device, and recording the result in this ADR once known. **The same test
   should also cover the escalation path in question 4**: a node holding a
   `tag:<id>-vpn` key that then runs `tailscale up
   --advertise-tags=tag:<id>-dev` must be rejected, not silently granted.
3. **Migration.** Given the answer to question 2: does `mksrv mesh`/`mksrv
   tenant mesh` retroactively re-tag every currently-registered (untagged)
   device on next join, all at once per tenant, or only going forward with
   the fallback rule carrying the untagged backlog indefinitely? If tags
   replace the user match, "only going forward" is not actually safe to
   leave half-done.
4. **`tagOwners` is the control against self-escalation — not a provisioning
   detail.** A pre-auth key's tag is set by whoever *issues* the key
   (`configd`/`mksrv`), but a client can also *advertise* a tag itself at
   `tailscale up` time (`--advertise-tags=tag:...`) — whether that succeeds
   is exactly what `tagOwners` governs, nothing else stops it. **Proposed
   answer**: `tagOwners` must list **no tenant Headscale user as an owner of
   any tag** — every `tag:<id>-*` gets set only via the pre-auth key at
   mint time (`configd` for `-dev`/`-vpn`, `mksrv mesh`/`mksrv tenant mesh`
   for `-svc`/`-host`) or by an explicit operator action (`headscale nodes
   tag`), never by the device itself. Without this, a `vpn`-tagged device
   could try to advertise its way into `tag:<id>-dev` and nothing in this
   proposal would stop it. Still open: where this config lives — part of
   `Policy()`'s generated HuJSON (a `tagOwners` block alongside `acls`) or a
   separate, rarer operator action — but the *shape* of the answer (no
   tenant user owns any tag) is settled, not open.
5. **Does this fully close the motivating case?** `tag:<id>-dev →
   tag:<id>-host:22` still relies on `sshd` (not the mesh ACL) to decide
   *which* commands/paths a given person reaches once connected — consistent
   with how ADR 0036's sidecar model already works, but worth stating
   explicitly rather than assuming.

## Consequences (sketch — fill in once a direction is picked)

Would make the RBAC groups ADR 0016 documents actually mean something at the
network layer, cap what a compromised service node can reach to exactly its
own data-plane dependencies, and stop a tenant's mesh membership alone from
being enough to reach an operator-hosted production port directly. Touches
`internal/headscale/headscale.go` (`Policy()`, `PolicyTenant`, tag rules,
`tagOwners`), `internal/configd/server.go` (mint with a tag derived from
`claims.Groups`, not a bare per-tenant-user key), `internal/cli/hosts.go`
(`joinLocalMeshNodes`, tagged key minting for `-svc`/`-host`),
`internal/deploy/mesh.go` (cloud fleet join, if fleet hosts also move to
tags), `mksrv tenant mesh` (ADR 0011). `docs/reference/rbac.md`'s footnote
already corrected to stop claiming an ACL-level restriction that doesn't
exist. Deferred until question 2 (does a tag replace the user match in this
Headscale version) is actually tested — it decides whether question 3's
migration can be gradual or has to be atomic per tenant.
