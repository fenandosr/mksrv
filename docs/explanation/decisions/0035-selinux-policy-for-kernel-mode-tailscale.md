# ADR 0035 (draft): SELinux policy for kernel-mode Tailscale

- Status: Proposed — open questions below, no decision yet
- Date: 2026-11-08
- Refines: ADR 0034 (tenant local hosts)

## Context

Both the cloud fleet's Tailscale unit (`internal/deploy/scripts/tailscale.container.tmpl`)
and the tenant-local `meshnode` stack (`stacks/meshnode/templates/mesh.container.tmpl`)
set `SecurityLabelDisable=true` on an enforcing host: `container_t` denies the
`tun_socket` operations a real kernel TUN device needs (`TS_USERSPACE=false`),
and disabling SELinux confinement for the whole container is the documented
workaround today.

A proof-of-concept exploring a tighter alternative found: a custom type
(`mesh_tun`), built from `udica`'s `base_container`/`net_container` templates
plus one additional rule block granting exactly the `tun_tap_device_t` /
`tun_socket` operations Tailscale needs, installed with `semodule -i`, and set
per-container with `SecurityLabelType=mesh_tun.process` instead of disabling
confinement outright. Confirmed on Rocky 9.8 / container-selinux 2.245,
enforcing, **rootless + kernel-mode**: `tailscale0` comes up, connectivity
works, no AVC denials. **Not yet tested with `meshnode` itself running
rootful** (the shape every `local_hosts` and fleet deployment today actually
uses — the PoC's rootless run is not representative of production).

Correction found after the PoC's first pass: "netfilter runs in iptables
mode" is inaccurate. In the rootless run, `tailscaled` cannot initialize the
`filter` table at all (`can't initialize iptables table 'filter'`) —
connectivity still works, but the `ts-input` rules Tailscale normally installs
never land. Worth evaluating `TS_DEBUG_FIREWALL_MODE=nftables` or
`--netfilter-mode=off` and documenting whichever actually applies once this
is tested rootful.

Fleet hosts already have an idempotent, versioned bootstrap step
(`BootstrapVersion`) where installing a policy module fits naturally.
`local_hosts` (ADR 0034) explicitly skip bootstrap — "no SELinux/firewalld/
Podman setup... the tenant's machine is assumed already podman+systemd-ready"
— so there is no existing hook to install a module on a tenant's own machine
before `meshnode` first deploys there.

## Open questions

1. **Fleet hosts**: bump `BootstrapVersion` and install the module
   unconditionally (cheap, inert if a host never runs tailscale-in-a-container),
   or gate it on whether any stack assigned to that host actually needs
   kernel-mode Tailscale?
2. **`local_hosts`** — three candidate shapes, not mutually exclusive:
   - **(a) Document only.** Add the exact `semodule` invocation to
     `docs/how-to/tenant-local-hosts.md` as a prerequisite, same tier as
     "assumed already podman+systemd-ready." Zero code; a skipped step fails
     silently as AVC denials, not a clean error.
   - **(b) `mksrv deploy <name>` installs it.** A narrow, `meshnode`-specific
     pre-check (needs the sudo `local_hosts` already uses for other things),
     not general bootstrap — closest to today's zero-bootstrap posture while
     still being safe by default.
   - **(c) Keep `SecurityLabelDisable=true` as the default**, offer the scoped
     type as an opt-in `meshnode` setting for an operator who already knows
     the host is SELinux-enforcing and wants it tightened — no installer,
     purely documentation plus a flag.
3. Does `meshnode` running **rootful** — the fleet unit's shape, and every
   `local_hosts` system Quadlet's shape (ADR 0034) — need anything different
   from the policy block the PoC tested? Untested; the PoC only ran rootless,
   which is not what production actually deploys. (Not a question about the
   *app* containers sharing `meshnode`'s network namespace — those are a
   separate concern from the policy module itself.)
4. Where does the module source live so it is versioned with the stack?
   **Done**: vendored at `stacks/meshnode/selinux/{base_container,net_container,mesh_tun}.cil`
   — `base_container.cil`/`net_container.cil` copied verbatim from the PoC
   host's `udica` package (`/usr/share/udica/templates/`, Rocky 9.8,
   container-selinux 2.245), `mesh_tun.cil` from the PoC itself. Removes the
   `udica` package as a host prerequisite entirely, for both the fleet and
   `local_hosts` paths. **Not yet wired into any deploy code** — no
   `stack.yaml` template entry, no `semodule -i` call anywhere. Loading them
   is still: `semodule -i base_container.cil net_container.cil mesh_tun.cil`,
   by hand, until question 2 is settled.

## Operator's recommendation (pending the rootful test)

- **Fleet hosts**: option 1, unconditional — install the module as part of
  bootstrap regardless of whether a given host ends up running
  tailscale-in-a-container. Cheap, and bootstrap is already the place
  one-time host setup belongs.
- **`local_hosts`**: option **(b)** (`mksrv deploy <name>` installs the
  vendored module as a `meshnode`-specific step) as the default, with **(a)**
  (documented manual prerequisite) as the fallback for a local host where
  that install can't run for some reason. **Not (c)** — `SecurityLabelDisable`
  should not stay the default once the module is vendored and installable;
  "opt-in tightening" leaves every tenant machine on the blanket disable
  unless someone remembers to ask for better.

This stays a recommendation, not a decision, until the rootful case is
confirmed.

## Consequences (sketch — fill in once a direction is picked)

Tightens the SELinux posture for kernel-mode Tailscale from a blanket disable
to a scoped type. Touches `stacks/meshnode/stack.yaml` (the three vendored
`.cil` files as stack assets, `local_hosts_only` deploy logic) and
`internal/deploy` bootstrap (fleet hosts). Deferred until the rootful case is
tested.
