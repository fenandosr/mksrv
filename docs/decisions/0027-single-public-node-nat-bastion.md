# ADR 0027: One public node — edge as NAT and bastion

- Status: Accepted
- Date: 2026-09-07
- Milestone: M27
- Refines: ADR 0013 (distributed topology)

## Context

ADR 0013 says "edge is the only public host," but the infrastructure gives
**every** fleet host its own Elastic IP: `aws_eip.host` in `infra/modules/aws-host`
is unconditional, and `infra/modules/network` routes all subnets straight at the
internet gateway ("No NAT gateway: hosts reach the internet through the internet
gateway using their own public IPs"). The `mksrv` CLI then SSHes to each host's
public IP directly.

Since February 2024 AWS bills every in-use public IPv4 address at
`$0.005/hour` (~`$3.65`/month), EIP or not. The distributed profile
(`core1` / `core2` / `core3` / `edge` / `appd`) therefore pays ~`$18`/month
just for public IPs — about a quarter of the deployment's monthly cost — and
only `edge` actually needs to be reachable from the internet.

The `core*` and `appd` hosts still need **outbound** internet: container image
pulls (GHCR, Quay, Docker Hub — which has no IPv6, ruling out an egress-only
IPv6 setup), `dnf`, the EC2/SSM instance APIs, AWS KMS for OpenBao auto-unseal,
and S3 for `backup`.

## Decision

### Topology

- **`edge` is the only host with a public IP.** It keeps its single EIP and
  gains two roles: NAT for the private subnets and SSH bastion.
- **`core*` and `appd` move to private subnets**, one per AZ, no EIP,
  `map_public_ip_on_launch = false`. `infra/modules/network` grows a private
  subnet per AZ alongside each public one; the CLI places every non-`base` host
  in a private subnet and `edge` in the public one.
- "Public" is **derived, not configured**: the host carrying `base` is `edge`
  (already how `infra/root` computes `base_host`). No new `deployment.yaml`
  field. A single-node fleet is unaffected — its one host carries `base`, so it
  stays public.

### edge as NAT

- Terraform sets `source_dest_check = false` on `edge`'s instance, adds a
  private route table with `0.0.0.0/0 → edge`'s ENI, and associates the private
  subnets with it. This route lives in `infra/root` (it needs `edge`'s network
  interface id), not the `network` module.
- The bootstrap script enables IP forwarding and installs an `nftables`
  masquerade rule for the VPC CIDR on `edge` only. `BootstrapParams` gains
  `NATForCIDR` (empty on every host but `edge`); `BootstrapVersion` bumps.
- An **S3 gateway VPC endpoint** (free) is added so `backup` traffic and image
  layers served from S3 skip the NAT path entirely.

### edge as bastion

- `internal/ssh`: `Target` gains an optional `Jump *Target`; `Dial` opens the
  jump connection first and tunnels the real connection through it (standard
  `ProxyJump`). No new keys — Terraform already attaches the operator's
  `MKSRV_SSH_PUBLIC_KEY` to every host, so the same key authenticates the jump
  and the target.
- `internal/cli` `openFleet`: non-`edge` hosts get `Target.Host` = their private
  IP and `Target.Jump` = the `edge` target. `infra.HostOutput.ManagementIP` for
  a private host becomes its private IP.
- `mksrv bootstrap` / `apply` order `edge` first so the jump host exists before
  the CLI reaches the inner nodes. Every other command (`deploy`, `mesh`,
  `tenant apply`, `status`, `postgres`/`openbao bootstrap`) inherits the jump
  transparently.
- Once `mksrv mesh` has run, all hosts are on the Headscale tailnet; connecting
  over `100.64.0.x` instead of the bastion is a possible later opt-in
  (`MKSRV_SSH_VIA=mesh`), not the default.

### Security groups

- The `mgmt_cidr → :22` ingress rule (including `mgmt_cidr: auto`) is applied to
  **`edge` only**. Inner hosts accept `:22` only from `edge`'s security group.
- `intra_vpc` (all TCP within the VPC CIDR) and `all egress` are unchanged — the
  VPC boundary stays the data-plane perimeter.
- Recovery from an operator IP lockout is still one `mksrv apply --infra-only`,
  now touching only `edge`'s SG.

## Consequences

- **Cost**: 4 fewer public IPv4 addresses — about `$14.6`/month (~`$175`/year)
  off the distributed profile, with no NAT Gateway (which at ~`$32`/month +
  `$0.045`/GB would cost more than the IPs it removes).
- **`edge` is a harder single point of failure.** The `core*` Patroni and
  OpenBao clusters keep running from local state if `edge` dies, but they lose
  outbound internet: a restarted OpenBao node cannot reach KMS to auto-unseal,
  and `mksrv apply` cannot reach the inner nodes until `edge` is back. `edge` is
  cattle — `mksrv apply --infra-only` + bootstrap rebuilds it in minutes, and
  OpenBao recovery keys are in SSM — but the window exists.
- **The NAT is a cross-AZ SPOF.** `edge` sits in one AZ; if that AZ fails, all
  three `core` nodes lose egress even though they are up. Accepted at this
  scale; the escalation path is a NAT instance per AZ (three times a negligible
  cost) or a NAT Gateway if control-plane HA becomes a requirement.
- **Cross-AZ data transfer**: inner-node egress now hops to `edge` before the
  IGW, so it is billed at `$0.01`/GB each way. Image pulls are a few GB/month —
  cents. The S3 endpoint keeps `backup` off this path.
- **Tooling touchpoints**: `internal/ssh` (`Target.Jump`, `Dial`),
  `internal/cli/hosts.go` (`openFleet` jump wiring, bootstrap ordering),
  `internal/infra` (private-host `ManagementIP`, an `edge` reference in the
  outputs), `internal/deploy` bootstrap (`NATForCIDR`, `BootstrapVersion`),
  `infra/modules/network` (private subnets), `infra/modules/aws-host`
  (conditional EIP, private `subnet_id`, `source_dest_check`, SSH SG source),
  `infra/root` (private route table → `edge` ENI, S3 gateway endpoint).
- **Migration**: not in-place. On the live fleet this is a destroy +
  `mksrv apply` of the infra layer (new subnets, `edge` keeps its EIP so the
  operator-zone DNS is unchanged; `core*`/`appd` get new private-only
  addresses). Fits the standing "destroy and rebuild, tenant data not
  preserved" stance for infra changes.
- Deferred: SSM Session Manager as the transport (AWS-native, no bastion, no
  inbound — but needs `amazon-ssm-agent` on the hosts, the session-manager
  plugin on the operator's machine, and still an SSM interface endpoint or the
  NAT for the agent's own traffic); a NAT instance per AZ; IPv6 egress.
