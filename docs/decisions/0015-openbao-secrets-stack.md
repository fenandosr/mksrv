# ADR 0015: OpenBao secrets stack (Raft HA, KMS auto-unseal)

- Status: Accepted
- Date: 2026-09-02
- Milestone: M12

## Context

mksrv has no secrets primitive of its own. Per-service credentials live only in
SSM Parameter Store (`/mksrv/{env}/<stack>/...`) and are pushed to hosts as
podman secrets. There is nowhere for team/human secrets, no per-tenant scoping
with policies, no encryption-as-a-service for PII columns, and no dynamic
credentials. It is the missing piece of the "multi-tenant plus" distributed
profile (`base + identity + postgres + OpenBao + logs + monitor`).

## Decision

### The `openbao` stack

A `kind: cluster` stack (odd ≥ 3 hosts, `depends_on: identity`) running
`ghcr.io/openbao/openbao` on **Integrated Storage (Raft)** — no external
etcd/Consul, mirroring the Patroni raft DCS choice in ADR 0013. Each node's
config lists a `retry_join` for every member (self included; OpenBao ignores its
own), so the cluster self-forms with no manual `raft join` step.

- **OpenBao, not Vault** — MPL-2.0, no BSL/usage restrictions; API- and
  CLI-compatible (`bao`).
- **Listener `tls_disable = 1`** — confidentiality is the mesh and the intra-VPC
  security group, the same boundary as Patroni/Postgres (ADR 0005). Port 8200 is
  published on the private VPC IP (cluster + raft) and the tailnet IP
  (operators, services); never `0.0.0.0`.
- **Storage** — a dedicated `baoraft` gp3 volume (`storage:` block, ADR 0014) at
  `/var/lib/mksrv/vol/baoraft`.

### AWS KMS auto-unseal

`seal "awskms"` with a fleet-level `aws_kms_key` (`alias/mksrv-<env>-openbao`,
key rotation on). Only hosts carrying `openbao` get the `kms:Encrypt` /
`kms:Decrypt` / `kms:DescribeKey` grant on their instance profile. Nodes unseal
themselves on every boot — no Shamir key ceremony, no operator step after a
reboot or redeploy, which matches mksrv's idempotent ethos. `bao operator init`
still produces **recovery keys** (5 shares, threshold 3) for recovery mode and
root-token regeneration; `mksrv openbao bootstrap` writes them and the initial
root token to SSM (`/mksrv/<env>/openbao/{recovery_keys,root_token}`,
`SecureString`). These are operator-only material and are never pushed as podman
secrets.

### `mksrv openbao`

- `bootstrap` — waits for the server, runs `operator init` if the cluster is
  uninitialised, stores the recovery material, waits for KMS auto-unseal on
  every node, waits for a converged Raft (one leader, N voters), then
  idempotently enables **KV v2** at `kv/` and the **AppRole** auth method.
  Records `.mksrv/openbao.json`.
- `status` — per-node seal state, Raft roles, enabled engines.

### Scope split

- **M12 (this ADR):** the HA cluster, KMS auto-unseal, KV v2 + AppRole.
- **M13:** per-tenant policy `tenant-<id>` over `kv/tenants/<id>/*`, a per-tenant
  AppRole with `role_id`/`secret_id` in SSM, the Transit engine + per-tenant keys
  for PII columns, internal PKI, OIDC auth wired to Keycloak realms, integration
  into `mksrv tenant apply`, and teaching a consumer (`database` / `postgrest`)
  to read from OpenBao instead of raw SSM.

## Consequences

- `HostOutput` gains `openbao_kms_key_id`; `render.Context` gains `Region` and
  `OpenBaoKMSKeyID`.
- A new fleet-level `aws_kms_key` / `aws_kms_alias` is created only when a host
  carries `openbao`. Destroying the stack leaves the key in its 14-day deletion
  window.
- Headscale's fleet service-port ACL gains `8200`.
- Deferred: TLS on the listener (auto-issued via a later PKI), dynamic database
  credentials, `bao` agent sidecars, per-node Raft snapshots to S3.

## M13 update

Per-tenant scoping landed: the descriptor is now `per_tenant: true` (a tenant
opts in via its `stacks:` list, like `cache`), and `mksrv tenant apply`
reconciles a `tenant-<id>` policy over `kv/tenants/<id>/*`, an AppRole bound to
it, and the RoleID/SecretID in SSM
(`/mksrv/<env>/openbao/approle_<id>_{role_id,secret_id}`). Transit (M14), OIDC
(M15), and the consumer refactor (M16) remain out of scope.
