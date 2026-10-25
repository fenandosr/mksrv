# ADR 0034: Tenant local hosts — `meshnode`/`gitea`/`codeserver` deployed in-process, no SSH

- Status: Accepted
- Date: 2026-10-25
- Milestone: M33

## Context

mksrv already knows two host categories: cloud-provisioned (`provider: aws`,
target `cloud`) and `existing` — a host the operator names in
`deployment.yaml` that mksrv doesn't provision but still reaches over SSH
(target `local`, confusingly overlapping in name only with this ADR).
Neither fits a tenant's own laptop or workstation: mksrv has no business
provisioning it, and it usually isn't SSH-reachable from the operator's side
at all (behind NAT, no public key exchanged, not always on).

Three apps want exactly this shape: a per-tenant Tailscale node
(`meshnode`) joining the tenant's own Headscale user, and Gitea /
code-server riding that node's network namespace so the tenant reaches them
over the mesh without opening anything else. All three only make sense
running *on* the tenant's machine, started *by* the tenant running `mksrv`
there themselves.

## Decision

### A third host category: tenant `local_hosts`

`tenants/<id>.yaml` gains `local_hosts: {<name>: {stacks: [...], ...}}`
(`internal/model.TenantLocalHost`). Each entry is a machine the tenant owns
and runs mksrv-managed stacks on directly — no SSH, no cloud provisioning, no
bootstrap (no SELinux/firewalld/Podman setup: the tenant's machine is assumed
already podman+systemd-ready). `mksrv deploy <name>` and `mksrv mesh`,
*run on that machine*, render and apply its stacks in-process.

`<name>` must equal the machine's own hostname and be globally unique across
every tenant's `local_hosts` — checked against both every other tenant's
entry and every `deployment.yaml` host name, since the same value doubles as
`fleet.byName`'s lookup key. `mksrv deploy <name>` has a single named target
and refuses to act (`"refusing to act on local host ... from ..."`) unless
`os.Hostname()` (or `MKSRV_LOCAL_HOST`, for a renamed machine or testing)
matches it — never a silent no-op. `mksrv mesh` has no single named target —
it scans every declared local host and silently skips whichever ones don't
match this machine (not a mistake, just not that instance's turn), joining
only the ones that do. Because of the deploy-side guard, and because a
cascading guard failure for every tenant's laptop would make an unqualified
`mksrv deploy` useless from any single machine, local hosts are **never**
part of that command's default "all hosts" selection — only reachable by
naming them.

### Reusing the stack catalog, not inventing a parallel one

`meshnode`/`gitea`/`codeserver` are ordinary `stacks/<name>/stack.yaml`
catalog entries, `targets: [local]`, marked `local_hosts_only: true` (a new
field: their templates read `Context.LocalHost`, which is only ever set for
a `local_hosts` entry — assigning one to `deployment.yaml` instead, where
`LocalHost` is always nil, is now a validation error rather than a template
execution crash). `local_hosts_only` is deliberately a separate flag from
`targets: [local]`: that value already means something else entirely for a
`deployment.yaml` host (an `existing`, still SSH-reachable one) and several
cloud-fleet stacks (`database`, `cache`, `monitor`, ...) declare it too for
that reason — `checkTenantLocalHosts` requires `local_hosts_only`
specifically, not just `local` in `Targets`, so a cloud stack can't be
listed under a tenant's `local_hosts` by mistake and get rendered with none
of the fleet context (`Peers`/`StackHosts`/a database connection) it expects.
`gitea`/`codeserver` declare `depends_on: [meshnode]` —
the existing dependency field and `orderedStacks()` already express "must
come after"; validated with a dedicated check (a `local_hosts` entry's own
dependencies must resolve within that same entry, not the fleet-wide
`assigned` map cloud stacks use).

`meshnode` is the catalog's first `multi_instance: true` stack — one tenant
local host can run several named Tailscale identities (`instance_key: name`
documents which sub-field carries the key). `render.Context` gains
`InstanceName`, handled exactly like `Tenant`/`{tenant}` already is for
`scope: per_tenant`: a `scope: "per_instance"` template is skipped when empty,
and `{instance}` in `dst` is substituted when set. The cross-field rule
("`multi_instance` requires `instance_key` and a `per_instance` template")
isn't expressible as a JSON Schema conditional, so it's enforced in Go
(`checkMultiInstanceStacks`), next to the existing `depends_on` cycle check.

### Deploying without SSH: a `Runner` interface

`deploy.DeployStack` and its helpers took a concrete `*ssh.Client`. They now
take `deploy.Runner` — `Run`/`RunInput`/`WriteFileSudo` — which `*ssh.Client`
already satisfies structurally (no changes needed there), mirroring the
narrower precedent `headscale.Runner` already set. `deploy.LocalRunner`
implements the same interface via `os/exec`, so `deployLocalHost`
(`internal/cli/hosts.go`) reuses 100% of the existing
render/secret/write/restart/hook/health-check machinery — it is not a
parallel deploy path, just a different `Runner`. Local stacks keep
system-level Quadlets and `sudo systemctl`/`sudo podman`, same as a cloud
host, authorized by the operator's own local sudo rights.

A `gitea`/`codeserver` secret ref may contain `{tenant}` (substituted by
`stackSecrets`, alongside the existing `{env}`) so each tenant's local host
gets its own SSM-backed secret rather than sharing one across every tenant's
instance that happens to deploy the same catalog stack.

### Joining the mesh: a podman Secret per instance, not per fleet

The cloud mesh join (`mksrv mesh` / `deploy.JoinMesh`) pushes one shared
Headscale pre-auth key as a single podman Secret (`mksrv-mesh-authkey`) used
by every cloud host's tailscale sidecar. A tenant's `meshnode` instance needs
its own: `mksrv-meshnode-<instance>-authkey`, since several instances can
live on the same local host.

The first design here tried a bare `Environment=TS_AUTHKEY` (no `=value`),
on the theory that podman would pass through whatever the invoking process's
own environment held for that name — letting `mksrv mesh` inject the key
with a plain `systemctl set-environment` and never write it to the unit file
at all. **Confirmed live against quadlet 4.9.3 that this does not work**:
quadlet's `Environment=` parser only keeps assignments that already contain
`=`; a bare name is silently dropped from the generated `ExecStart=`
entirely — no `--env TS_AUTHKEY` of any kind reaches `podman run`. The key
never got into the container under any circumstance.

The working mechanism is the podman Secret the cloud join already uses, just
scoped per instance: `mksrv deploy <name>` creates
`mksrv-meshnode-<instance>-authkey` **empty** on first deploy (so the
Quadlet's `Secret=...,type=env,target=TS_AUTHKEY` directive has something to
mount and the container can start at all — tailscale just sits in
`NeedsLogin`, not a crash). `mksrv mesh`, run later on the tenant's own
machine, pushes the real pre-auth key with `--replace` and restarts the
unit — using the same per-tenant Headscale user it already creates for
Cloud-IT VPN clients. "Already joined" is checked against tailscale's own
state (`podman exec mesh-<instance> tailscale ip -4`), not the systemd
unit's active/inactive state: the container (and its unit) stay "active"
indefinitely even stuck in `NeedsLogin` with an empty key, so unit state
alone can't tell a real join from one that never happened. `mksrv deploy
<name>` must run at least once, creating the unit, before `mksrv mesh` can
join it — restarting a unit that doesn't exist yet fails with a clear
systemd error naming the missing unit.

## Consequences

- **A tenant's own machine is now a first-class, if minimal, deploy target.**
  It never bootstraps, never shows up in `mksrv status` (no SSH session to
  check), and is excluded from `mksrv host trust`/`bootstrap`/fleet-wide
  apply — all of those are no-ops or skips for a `Local` host, by design.
- **The hostname safety guard is the only thing standing between "wrong
  tenant's laptop" and a confusing partial deploy.** It's deliberately strict
  (exact match, case-insensitive) and overridable only via an explicit env
  var, never silently.
- **`gitea`/`codeserver` share their meshnode instance's network namespace**
  (`Network=container:mesh-<name>`) rather than publishing ports — reachable
  only via that instance's Tailscale IP, not `127.0.0.1` on the host. Their
  podman-native `HealthCmd`s run inside that shared namespace for the same
  reason; there is no host-level `health:` check for either (the stack-level
  check that exists for every other stack would curl a port the host can't
  see).
- **A local host's name also has to be unique against `deployment.yaml`'s
  own host names**, not just against other tenants' `local_hosts` — it's the
  same `fleet.byName` lookup key, and a collision would silently steal a
  real cloud host's entry out from under it (`mksrv deploy base` landing on
  a tenant's laptop instead of the edge). Checked case-insensitively, same
  as the hostname guard.
- Touchpoints: `schemas/stack.v1.json` (`multi_instance`, `instance_key`,
  `local_hosts_only`, `scope: per_instance`), `schemas/tenant.v1.json`
  (`local_hosts`), `internal/model.go`, `internal/render/render.go`,
  `internal/deploy/stack.go` + new `local_runner.go`,
  `internal/cli/hosts.go` (`hostTarget.Local`, `deployLocalHost`,
  `renderLocalContext`, the `mksrv mesh` extension), `internal/workspace/semantic.go`,
  three new catalog entries.
- Deferred: a generic multi-instance dispatcher (today `deployLocalHost`
  special-cases `meshnode` by name — fine with exactly one such stack, worth
  revisiting with a second); rootless/user-systemd local stacks (everything
  here assumes the tenant has local sudo, same as a cloud host); self-service
  `local_hosts` management tooling beyond hand-editing the tenant YAML.
