# ADR 0044: A single-host endpoint that follows the Patroni leader

- Status: Accepted (code merged; **not deployed** until the operator adds the stack to a host and confirms)
- Date: 2026-11-20

## Context

Every consumer of the Patroni cluster reaches it with a libpq multi-host string and
`target_session_attrs=read-write`: PostgREST, the secrets mirrored for tenants
(`hosts`, `url`). That works for clients built on libpq and fails for any client that
accepts one `host:port`. Prisma's engine rejects the multi-host form
(`invalid port number in database URL`, reported by the SciNugget developers, verified
locally with a fake string, not against the cluster), and other Node ORMs share the
limitation. Such a tenant had no supported way to reach the primary.

Three facts constrain the answer (all checked, 2026-11-20):

- No proxy, virtual IP or stable name to the leader exists in the fleet.
- Patroni's REST API tells the leader apart: `GET /primary` is `200` on the leader and
  `503` on a replica (seen live on the three nodes).
- That REST API has **no authentication configured** and listens on the VPC and
  loopback only; the tenants' Headscale ACL does not open its port. Anyone who can
  reach it can ask for a switchover, a restart or a configuration change, so it must
  not be exposed to tenants.

## Decision

A new fleet-wide stack, **`pgproxy`**: HAProxy (3.0 LTS, pinned) on a host inside the
VPC (`appd` in this fleet) that listens on **5433** and forwards each connection to the
current leader.

- The leader is found by an HTTP health check against each node's Patroni REST
  (`GET /primary`, expect 200) **from inside the VPC**; the data connection goes to
  PostgreSQL on 5432. The REST API is never published.
- `inter 2s fall 2 rise 2` and `on-marked-down shutdown-sessions`: a failover is noticed in
  a few seconds and the old leader's sessions are closed, so clients reconnect (a pool does
  it by itself) instead of writing to a replica. Idle timeouts are long (8 h) so a pooled
  connection is not cut by the proxy.
- Published like the other tailnet-reachable services: the host's VPC address, its tailnet
  address, and loopback. Port **5433** is added to the ports tenants may reach on the
  fleet (`headscale.Policy`).
- A local `/healthz` on the host's loopback (port 8404) returns 200 only while a leader is
  known, and is what the deploy health check and the container health check use.
- The configuration lists every `postgres` stack node and is restarted when it changes
  (a hook compares the hash of the rendered file; the first deploy only records it).
- **Secrets:** on a cluster with a `pgproxy` host, the `database` secrets (`database`,
  `database-<role>`, `database-<id>_auth` when published) gain `host_single` and
  `url_single` (`postgres://user:pass@<proxy>:5433/db_<id>`, no query string, the app adds
  its own). `url` and `hosts` are unchanged. The mirror now rewrites an entry when any
  field is new or changed, not only when the password changed, so existing tenants pick the
  new fields up on the next `tenant apply`.

## Alternatives rejected

- **Open Patroni's REST port to tenants** so each could pick the leader: unauthenticated
  mutating endpoints. No.
- **A proxy on each Patroni node:** still several hosts for the client to choose from.
- **A DNS record moved by a Patroni callback:** needs AWS credentials on the database nodes
  and waits on TTLs and caches.
- **The client picks the leader at start-up** (`pg_is_in_recovery()`): a failover then needs a
  restart of every client.

## Consequences

- A tenant whose ORM takes one host uses `url_single`; one that handles multi-host keeps
  `url`. Reads on replicas still need the multi-host form.
- **The proxy host is a single point of failure for this path.** Today `appd` already is
  for PostgREST, Redis and the monitoring; the multi-host `url` remains for anything that must
  not depend on it.
- PostgreSQL sees the proxy host's VPC address as the client (`10.20.0.0/16` in `pg_hba`).
  Connections through this path therefore stop depending on the Tailscale masquerade that
  the `samenet` line relies on today (**to verify** during the rollout).
- Plaintext inside the mesh, as before.
- **Not verified:** behaviour in a real failover of this cluster, added latency, and that the
  rendered HAProxy configuration loads (checked by the first deploy).
- Adjacent, not done here: Patroni's REST API should get `restapi.authentication`.

## Rollout

Add `pgproxy` to the proxy host's stacks in `deployment.yaml`; `mksrv deploy <host> --stack
pgproxy`; check `/healthz` and a real connection on 5433; then `mksrv tenant apply` so the
ACL and the mirrored secrets are refreshed.
