# ADR 0013: `kind: cluster` stacks and the Patroni PostgreSQL cluster

- Status: Accepted
- Date: 2026-09-01
- Milestone: M9 (prototype of the distributed profile)

## Context

Every stack so far is a single instance per host (`service`). A distributed / HA
profile — quorum-based data services spread across AZs — is the intended second
deployment shape. Its keystone is an HA PostgreSQL cluster that identity,
monitor, and database will eventually consume instead of their own embedded
Postgres (ADR 0004 already calls Keycloak DB durability "mandatory").

This ADR covers the prototype: the cluster working with automatic failover, and
the two engine primitives it forces.

## Decision

### `kind: cluster` stacks

`stack.yaml` gains `kind: service | cluster` (default `service`). A `cluster`
stack:
- must be assigned to an **odd number of hosts ≥ 3** (`semantic.go`
  `stack.cluster.count`);
- deploys its unit to every assigned host through the normal per-host loop —
  each host renders with its own context;
- self-organises at runtime (the prototype uses Patroni's raft DCS — no external
  etcd/consul);
- is finalised by a dedicated reconcile command, not by `mksrv apply`.

New render primitives (generalising `StackIP`): `Context.StackMembers`
(stack → every carrier host: name, private IP, tailnet IP) with
`.StackNodes(stack)` / `.StackPeers(stack)` (peers = nodes minus self).

### The `postgres` stack

- Image `ghcr.io/fenandosr/mksrv-postgres` — `postgres:16-bookworm` + `patroni`
  + `python3-pysyncobj` + `pgbackrest`, built in CI like configd. pgBackRest
  binary is present; its config is a follow-up.
- Patroni raft DCS (`self_addr` / `partner_addrs` from `.StackPeers`), REST API
  on `:8008`, Postgres on `:5432`, published on the private VPC IP (cluster
  traffic uses the aws-host `intra_vpc` SG rule), the tailnet IP, and loopback.
- Per-host health check hits Patroni `/liveness` (up before quorum) so the
  sequential `mksrv apply` does not stall on the first two nodes.

### `mksrv postgres bootstrap`

Waits for `patronictl list` to show one leader + healthy replicas, creates a demo
`app` role + database on the primary, records `.mksrv/postgres.json`
(`{scope, primary, nodes[]}`), and prints the consumer DSN
(`host=ip1,ip2,ip3 target_session_attrs=read-write` — modern libpq / JDBC follow
the leader without HAProxy). `--switchover` forces a failover to demonstrate it.

### Multi-AZ network

`infra/modules/network` gains `subnet_count` (`count`-indexed subnets, one per
AZ, with `moved` blocks so the live single subnet is not destroyed). The CLI
derives it: `3` when any assigned stack is `kind: cluster`, `1` otherwise — no
new `deployment.yaml` field. `infra/root` round-robins AWS hosts across the
subnets by sorted name.

## Consequences

- The live 2-node deployment is untouched: `subnet_count` stays 1, the `moved`
  blocks fire once with zero resource churn.
- Deferred: **pgBackRest** (S3 bucket + host IAM S3 policy + stanza + WAL
  archiving), the **consumer refactor** (`provisionDatabases`,
  `reconcilePostgREST`, `keycloak.container.tmpl` hard-code `mksrv-postgres` /
  `mksrv-identity-postgres` today), the **OpenBao** cluster stack,
  `identity`/`monitor` distributed rendering, folding `postgres bootstrap` into
  `apply`, Route53 multivalue for the stateless tier, and a `deployment.yaml`
  `topology` field with host-model quorum awareness.
- ADR 0004's "one Keycloak instance" stays; only its database moves to the
  cluster in the consumer-refactor milestone.
