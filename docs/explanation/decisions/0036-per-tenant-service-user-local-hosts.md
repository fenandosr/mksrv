# ADR 0036 (draft): a per-tenant service user, for un-sudo'd deploys on an operator-shared host

- Status: Proposed — open questions below, no decision yet
- Date: 2026-11-08
- Refines: ADR 0034 (tenant local hosts)

## Context

ADR 0034 ships `local_hosts` for "a machine the tenant owns" and explicitly
defers "rootless/user-systemd local stacks" — every `local_hosts` stack,
including `meshnode`, runs as system-level Quadlets, assuming the tenant has
local sudo on their own box, the same as a cloud host's.

A different, real shape has come up since: an **operator-administered** host
that already serves one tenant's production workload, where a **second
party** — that tenant's own developer — needs to deploy and iterate on their
own app on that same box, without broad sudo, and without the operator
touching it deploy-to-deploy.

`Network=container:mesh-<instance>` — how an app shares a `meshnode`
instance's network namespace — only works when both containers belong to the
**same** Podman context: rootless-to-rootless, or rootful-to-rootful. A split
where `meshnode` is a root-owned system Quadlet and the developer's app is a
plain unprivileged rootless container cannot join that namespace at all (confirmed
directly: rootful and rootless Podman are separate stores and separate kernel
namespaces; a rootless process has no path to attach to a root-owned network
namespace).

A proof-of-concept is exploring a model that would avoid both broad sudo and
the rootful/rootless split: a **dedicated service user per tenant**
(`svc-<id>`, `nologin` shell, `loginctl enable-linger`, its own subuid/subgid
range) that would own *both* the `meshnode` instance and the app's
containers, entirely rootless, entirely under that one unprivileged account.
The developer never gets a shell as `svc-<id>` — `nologin` means that account
has none. Instead they'd talk to their own sidecar process over a socket,
with a constrained set of verbs, and that sidecar executes the actual
commands as `svc-<id>`. **That sidecar and its verb set are entirely the
operator's own tooling, outside mksrv** — mksrv's only involvement is
whatever runs `mksrv deploy <name>` as `svc-<id>` once asked to.

**Whether `Network=container:mesh-<instance>` actually works between two
rootless containers owned by the same non-root user is not yet verified by
the PoC — treat it as the model's central hypothesis, not a confirmed fact,
until tested.**

**Not implemented. This ADR is a draft to settle the open questions below
before any code changes.**

## Open questions

1. **Schema.** Does this need a new field on a `local_hosts` entry (something
   like `run_as: <service-user>`), defaulting to today's implicit behavior
   ("whoever invokes `mksrv deploy <name>`") when absent? Or is it really a
   property of the `meshnode` stack specifically, since that stack is what the
   network-namespace-sharing constraint actually hinges on?
2. **Execution.** `deploy.Runner` / `deploy.LocalRunner` (ADR 0034) run as
   whoever invokes the CLI. `svc-<id>` is `nologin` — the developer never
   runs `mksrv deploy <name>` themselves, so "the dev logs in as the service
   user" is not the model. The only question mksrv actually has to answer is
   narrower: does `Runner` need an implementation that execs as `svc-<id>`
   specifically (e.g. a `sudo -u svc-<id>` wrapper around today's
   `LocalRunner`), invoked by whatever the operator's own sidecar calls on
   the developer's behalf? Everything about how the developer authenticates
   to that sidecar and what verbs it exposes is outside this question, and
   outside mksrv.
3. **Provisioning the service user itself** (the account, the subuid/subgid
   range, enabling linger) — a one-time manual step by the operator, documented
   at the same tier as "assumed already podman+systemd-ready," or a new,
   narrow, bootstrap-like step specific to this one case?
4. **Naming / scope.** ADR 0034 frames `local_hosts` as "a machine the tenant
   owns." This shape is the opposite: an **operator-owned** host serving one
   tenant, with a second, less-trusted party needing deploy access to it. Is
   that the same `local_hosts` concept with a new ownership axis, or does it
   want its own category — a third host shape, or a `local_hosts` sub-mode?
5. **Blast radius.** If the service user's credentials leak, the claim is that
   the damage is scoped to that one unprivileged account's own containers and
   subuid range. Worth stating as the model's central security property and
   confirming it actually holds on a shared host — ordinary process isolation
   between Linux users is all that separates one tenant's service user from
   another's (and from the host's other, unrelated workloads) once nothing is
   containerized at the host level itself.

   That's not the whole boundary, though: `svc-<id>`'s `meshnode` instance
   *is* the tenant's actual network identity on the mesh. Compromising
   `svc-<id>` doesn't just hand over one Linux account's containers — it hands
   over whatever that tenant's Headscale ACLs allow its devices to reach,
   including the shared fleet ports (`fleetPorts` in
   `internal/headscale/headscale.go`'s `Policy()`: Postgres, OpenBao, Redis,
   …). The real boundary is the narrower of (Linux user isolation on the
   host) and (that tenant's own ACL reach on the mesh) — state both, not just
   the first.

## Consequences (sketch — fill in once a direction is picked)

Would give mksrv a third answer to "who can deploy here, with what
privilege" alongside today's two (`existing`: operator SSHes in with full
access; `local_hosts`: the tenant has full local sudo on their own box) — a
host the operator administers, serving one tenant, where a second,
narrower-trusted party deploys without sudo at all. Touches
`schemas/tenant.v1.json` / `internal/model.TenantLocalHost` (if a schema field
is needed), `internal/deploy` (if a new `Runner` is needed), and
`docs/how-to/tenant-local-hosts.md`. Deferred until the open questions above
are answered.
