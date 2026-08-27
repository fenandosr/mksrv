# Architecture

## Engine and workspace

`mksrv` is split into a public, distributable engine and a private,
operator-owned workspace. The binary embeds `infra/`, `stacks/`, and `schemas/`
and extracts them atomically into a versioned user cache. Terraform data,
generated provider/backend configuration, outputs, locks, and secrets remain in
the workspace.

## Responsibility split

| Layer | Responsibility | Lifecycle |
|---|---|---|
| Terraform | Hosts, disks, EIPs, security groups, IAM, SES, DNS, remote state | Infrequent |
| CLI over SSH | Rocky Linux bootstrap, Podman, storage mount, firewall, mesh join | On add/on demand |
| Stack deploy | Render, checksum-aware transfer, Quadlet activation, hooks, health | Frequent |
| Reconciliation APIs | Headscale users/keys and Keycloak realms/groups/users | Frequent |

Container and tenant lifecycle do not belong in Terraform state. Terraform ends
when a host exists, is reachable, has Podman, and can join the mesh.

## Mesh backplane

Headscale/Tailscale provides the private backplane. Cross-stack traffic uses
MagicDNS names rather than public addresses. Existing hosts can remain behind
NAT because they join outbound; management still uses an explicit operator-known
address.

## Apply sequence

1. Apply infrastructure.
2. Bootstrap and deploy the host carrying `base` and `identity`.
3. Start Headscale and mint short-lived, one-use pre-authentication keys.
4. Bootstrap, join, and deploy remaining hosts in dependency order.
5. Reconcile tenant realms, users, databases, vhosts, and DNS.

## Host support

The CLI is cross-platform, but v1 fleet hosts are Rocky Linux 9 only. Later
deploy code must fail clearly on unsupported distributions rather than attempting
best-effort package installation.
