# Tenant local hosts (`local_hosts:`)

A tenant's own laptop or workstation can run `meshnode`, `gitea`, and
`codeserver` directly — mksrv never provisions or SSHes to it (ADR 0034).
`mksrv deploy <name>` and `mksrv mesh`, run **on that machine itself**, render
and apply its stacks in-process.

```yaml
# tenants/<id>.yaml
local_hosts:
  wsl-laptop:                          # must equal the machine's own hostname
    stacks: [meshnode, gitea, codeserver]
    meshnode:
      instances:
        - name: work
    gitea:
      mesh: work
    codeserver:
      mesh: work
```

| field | |
|---|---|
| the map key (`wsl-laptop`) | must equal the machine's real hostname (`hostname` at a shell), and be unique across **every** tenant's `local_hosts` — it's what `mksrv deploy`/`mksrv mesh` check this machine's own identity against before touching anything. |
| `stacks` | any catalog stack with `local_hosts_only: true` — today `meshnode`, `gitea`, `codeserver`. |
| `meshnode.instances[].name` | one Tailscale identity per entry. `gitea`/`codeserver` each join exactly one instance's network namespace via `mesh`. |
| `gitea.mesh` / `codeserver.mesh` | must name one of `meshnode.instances[].name` in this same `local_hosts` entry. |

`gitea`/`codeserver` both declare `depends_on: [meshnode]` — leaving
`meshnode` out of `stacks` while listing either of them is a validation
error, same as any other unmet catalog dependency.

## Running it

On the machine itself (its `hostname` must match the key above, or set
`MKSRV_LOCAL_HOST` to override), **in this order**:

```sh
mksrv deploy wsl-laptop # renders + starts meshnode, gitea, codeserver
mksrv mesh              # joins this host's meshnode instance(s) to the tailnet
```

`deploy` must run first: it creates the `mesh-<instance>.service` unit (and
an empty placeholder for its auth-key secret, so the container can start at
all — tailscale just sits unauthenticated until `mesh` runs). Running `mesh`
before anything has ever been deployed fails trying to restart a unit that
doesn't exist yet.

`mksrv deploy <name>` refuses to act — with a clear error, never a silent
no-op — if run from a machine whose hostname doesn't match `<name>`, and
isn't part of its own default "all hosts" selection: an unqualified `mksrv
deploy` (no arguments) only ever touches the cloud fleet, never a tenant's
local hosts. `mksrv mesh` has no single named target the same way — it
scans every tenant's `local_hosts` and silently joins whichever ones match
*this* machine, skipping the rest (not an error, just not their turn); it's
always safe to run on any machine, local host or not.

A local host never bootstraps (`mksrv host bootstrap`), is never trusted for
SSH (`mksrv host trust`), and never appears in `mksrv status` — all three are
no-ops or skips for it. It's assumed to already have Podman + systemd
working, the same prerequisite a cloud host's bootstrap step would otherwise
set up.

## Reaching gitea/codeserver

Neither publishes a port on the host: each shares its `meshnode` instance's
network namespace (`Network=container:mesh-<name>`), so they're reachable
only via that instance's Tailscale IP — `https://<tailnet-ip>:3000` for
Gitea, `:8080` for code-server — from any device joined to the tenant's
Headscale user (the same one `forwards:`/`dns:` already use for
Cloud-IT VPN clients and real Tailscale).

Gitea's initial admin user (`admin`) is seeded automatically on first
deploy, with a password mksrv generates and stores the same way as every
other stack secret. code-server's password is generated the same way.

## Trade-offs

- **No SSH means no remote troubleshooting.** If the tenant's machine is off
  or unreachable, there's nothing the operator can do from the fleet side —
  by design; this is infrastructure the tenant owns, not the fleet's.
- **The hostname guard is strict and literal.** A machine reinstalled with a
  different hostname needs either the `local_hosts` key renamed to match, or
  `MKSRV_LOCAL_HOST` set for every `mksrv` invocation there.
- Assumes the tenant's own machine has local sudo rights configured — the
  same system-level Quadlets and `sudo systemctl`/`sudo podman` a cloud host
  uses, just executed in-process instead of over SSH.
