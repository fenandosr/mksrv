# ADR 0011: Tenant-owned infrastructure — forwards, per-tenant DNS, mesh enrolment

- Status: Accepted
- Date: 2026-09-01
- Milestone: M7

## Context

A tenant may run infrastructure mksrv does not provision — an HPC cluster, a
JupyterHub, a login node behind a Ceph store. The tenant needs to:

1. publish a name in its own apex domain (`jupyter.mcps-epcm.org`), not the
   operator domain;
2. reach its own nodes through the Cloud-IT VPN tunnel;
3. attach those nodes to the Headscale mesh under its own Headscale user.

Until now `deployment.yaml` only modelled hosts mksrv creates (`provider: aws`)
or the operator's own hosts (`provider: existing`). The tenant schema carried an
unused `dns_override`; `configd`'s forward set (`demoForwards`) was hard-coded to
fleet services; nothing minted tenant-scoped Headscale pre-auth keys.

## Decision

### Three optional tenant-document blocks

- **`forwards`** — a list of `{id, label, type (http|tcp|ssh), target, open?,
  path?, ssh_alias?}`. `mksrv tenant apply` translates each to a `configd.Forward`
  and appends it to the tenant's broker roster entry, after the built-in fleet
  forwards. `target` is a `host:port` MagicDNS name of a tenant mesh node. No
  change to `configd` (the roster already carries `forwards` verbatim) or to
  `cloud-it-vpn` (its `tenant.json` never held forwards, and it already accepts
  `ssh` forwards). Ids `edge-health`, `database`, `rest`, `cache` are reserved.
- **`dns`** — a list of `{name, type (A|AAAA|CNAME), value, ttl?}` records mksrv
  writes into the tenant's **own** hosted zone (`dns_override.zone_id`), never
  the operator zone. The Terraform root iterates tenants and calls the existing
  `modules/dns` once per tenant zone with `allow_overwrite = false`: a name that
  already exists in the tenant zone fails the apply rather than being clobbered.
  Only `route53` `dns_override` is supported for now.
- **`mesh_routes`** — a list of CIDRs the tenant's nodes may advertise. Each adds
  an ACL rule (`<id>@ -> <cidr>:*`). Route *approval*
  (`headscale nodes approve-routes`) stays a manual operator step.

### `mksrv tenant mesh <id>`

Mints a Headscale pre-auth key under the tenant's Headscale user (`<id>`) and
prints the `tailscale up` command to run on the tenant-owned node. The node then
appears as `<hostname>.<env>.mksrv` and can be named in `forwards[].target`. The
existing ACL rule `<id>@ -> <id>@:*` already lets the tenant's VPN devices reach
it; no ACL change for the direct case.

### Web endpoints: tenant terminates TLS (Model B)

For `jupyter.mcps-epcm.org` and similar, the tenant's cluster runs its own
ingress and TLS; mksrv only creates the DNS record pointing at the cluster's
public address. The mksrv edge is **not** inserted into the tenant's web data
path, so no fleet→tenant ACL opening and no edge Caddy vhost for tenant web
services. The VPN tunnel carries only SSH and other private-protocol access.

## Consequences

- `schemas/tenant.v1.json`, `internal/model.Tenant`, and
  `internal/workspace/semantic.go` gain the three blocks and their validation.
- `infra/root` gains `module "dns_tenant"` (`for_each` over tenants) and a
  `dns.tenant_created` output. `modules/dns` is unchanged.
- The tenant's Route53 zone now holds mksrv-managed records alongside the
  tenant's own; `allow_overwrite = false` keeps mksrv from touching anything it
  did not create, including mail records.
- Automatic subnet-route approval, Model A (edge terminates TLS for tenant web
  endpoints), Keycloak↔cluster identity federation, and non-route53 tenant DNS
  providers are out of scope.
- The `domain.outside_root` validation message no longer references "M6".
