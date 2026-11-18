# ADR 0042: OpenBao runs with host networking, so it sees its clients' real addresses

- Status: Accepted (code merged; rolled out node by node, see Rollout)
- Date: 2026-11-18

## Context

`token_bound_cidrs` / `secret_id_bound_cidrs` on an AppRole (ADR 0041 binds
`svc-hgstg-lims-worker` to `100.64.0.11/32`) are only as good as the source
address OpenBao sees. A login from `ome` (tailnet `100.64.0.11`) failed with
`source address "10.89.1.1" unauthorized by CIDR restrictions on the role`, and
the audit log recorded every tailnet client as `10.89.1.1`, the gateway of the
`mksrv-openbao` podman bridge.

Diagnosed on `core1` (read-only): netavark's DNAT for the published port does
**not** rewrite the source — connections from `appd` over the VPC address reach
the container with their real address (`/proc/net/nf_conntrack` reply
destination equals the client). The rewrite happens only for traffic that
arrived on `tailscale0`: the node's `tailscaled` (a container in kernel mode)
installs legacy iptables rules that mark everything entering via `tailscale0`
(`ts-forward`: `-i tailscale0 -j MARK 0x40000`) and `MASQUERADE` it on the way
out (`ts-postrouting`). A DNAT'd packet is forwarded traffic, so it matches, and
the container sees the bridge gateway.

## Alternatives rejected

- **Turn tailscaled's SNAT off fleet-wide** (`--snat-subnet-routes=false`). It
  removes the masquerade for every published port on the host, including
  Patroni's. The cluster's `pg_hba` only allows `10.20.0.0/16`, `127.0.0.1` and
  `samenet`, and tailnet clients currently authenticate *because* they arrive as
  the bridge gateway (`10.89.2.1`, `samenet`). With their real `100.64.x.y`
  address, every tenant's Postgres access over the tailnet would be rejected.
- **A proxy and `x_forwarded_for_authorized_addrs`.** There is no proxy in front
  of OpenBao.
- **Rootful netavark.** Already the case; it is not what rewrites the source.
- **An nft rule clearing tailscale's `0x40000` mark for `tailscale0 → podman:8200`.**
  Small, but depends on an internal detail of Tailscale.
- **Widening the role's CIDR to `10.89.1.1/32`.** Accepts any tailnet client.

## Decision

The `openbao` quadlet uses `Network=host` and publishes nothing. No DNAT, no
masquerade, on any path: the client's real address reaches OpenBao.

- **Listeners are explicit, never `0.0.0.0`:** the host's VPC address (also the
  Raft `cluster_address`, port 8201), `127.0.0.1` (`mksrv` runs `bao` inside the
  container against loopback), and the tailnet address when the host has one.
- **firewalld.** A published port bypassed `INPUT` (`ct status dnat accept`);
  host networking does not. A rendered `firewall.sh`, run as `ExecStartPre`
  (so the rules exist before the container binds), adds source-scoped rich rules
  to the `public` zone, runtime and permanent, idempotently: `8200/tcp` from each
  fleet host's VPC address and from `100.64.0.0/10`; `8201/tcp` only from the
  other OpenBao nodes. Never a bare port. Stale rules are not pruned when a peer
  disappears.
- The `mksrv-openbao` bridge network and its unit are no longer rendered. An
  already-deployed host keeps the orphaned `mksrv-openbao.network` file until it
  is removed by hand; it is harmless.
- `After=mksrv-tailscale.service` so the tailnet address exists when the
  listener binds (`Restart=always` covers a race).

## Rollout

OpenBao is the cluster's secret store (3 Raft nodes, KMS auto-unseal), so this
goes one node at a time, followers first and the leader last, checking
`bao operator raft list-peers` and `bao status` after each, and an AppRole login
from `ome` at the end. A node whose container fails to bind is rolled back by
redeploying the previous revision of the stack. Not testable in CI: SELinux must
let `container_t` bind `trivnet1_port_t` (8200), and the firewalld rules must
apply from a systemd-launched script; both are verified live on the first node.

## Consequences

- CIDR-bound AppRoles work for tailnet clients, and the audit log records real
  client addresses.
- During each node's restart it is briefly out of the quorum (3 nodes tolerate
  one).
- 8201 and the tailnet/loopback cluster listeners: OpenBao binds a cluster port
  per listener; only the VPC one is reachable (firewalld), the others are inert.
- The same masquerade still affects every other published tailnet port
  (Patroni, Keycloak, Prometheus, …). It is intentional for Postgres today;
  fixing it elsewhere means revisiting `pg_hba` first.
