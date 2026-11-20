# Case: HG-LIMS

A laboratory information system for a genomics lab: a SPA, a worker, a Postgres
database behind PostgREST, S3 storage for large sequencing files and, later, an
Airflow for heavy pipelines. Developed by the tenant's own team; runs on `ome`
(a node the tenant owns) and uses the fleet's services. Staging tenant:
`hgstg`. Status: **in production use on staging**. Follows the
[generic blueprint](../tenant-app-on-fleet.md).

## 1. Timeline

| What | Outcome | mksrv change |
|---|---|---|
| An S3 bucket, an IAM identity and an AppRole for the worker | Done declaratively: bucket with a fixed secure posture, a dedicated IAM user, the key mirrored to OpenBao, a read-only AppRole bound to the node | `storage:` block, [ADR 0041](../../explanation/decisions/0041-tenant-storage-s3-and-approles.md) |
| The AppRole login failed: `source address "10.89.1.1" unauthorized by CIDR restrictions` | Root cause was not the container network but tailscaled's own masquerade on the OpenBao hosts | OpenBao on host networking, [ADR 0042](../../explanation/decisions/0042-openbao-host-networking.md); rolled out one node at a time |
| An Airflow for heavy workflows | Asked first as one Airflow for the whole fleet; **reconsidered** as one dedicated to the app, on the node, next to the data | See below |
| The app's worker needed a database credential with more reach | Published on explicit opt-in, with the risk written down | [ADR 0039 addendum](../../explanation/decisions/0039-tenant-roles-postgrest-keycloak-clients.md) |
| New functions were invisible to the API after a migration | Rule: end every migration with `NOTIFY pgrst, 'reload schema';` | Documented in the developer guide |

## 2. The Airflow decision, in short

The first request was a fleet-wide Airflow with per-tenant isolation "by
discipline", an edge executor for a remote worker, and Airflow's UI as the
operator's global observatory. Putting it next to the data changed most of that:

| Topic | Fleet Airflow | Dedicated, on the node |
|---|---|---|
| Executor | local, then a remote edge worker | local: the tasks are already beside the data |
| Isolation between tenants | conventions, pools, queues | not applicable: one app |
| A credential limited to one tenant's DAGs | hard | disappears |
| Remote task logs | needed | not needed |
| Operator's global view | Airflow's UI | **Grafana** |
| Metadata database | on the cluster | a local Postgres: the cluster's nodes are small and also hold the secret store; the history is cheap to lose |

Decisions: the observatory is Grafana; metadata in a local Postgres; the DAGs
talk to the app's database through a dedicated service role (no PostgREST); the
UI listens only on the tailnet; the DAGs reach the node as a local bundle through
the deploy agent, so no GitHub credential is needed on the node.

## 3. Lessons that went into the blueprint

- Ask **where it should live** before designing around a central service.
- A CIDR binds a host, not an application.
- Roll out changes to a shared secret store one node at a time, followers first,
  and verify quorum after each.
- A wrapped SecretID is single use and expires: mint it when you will place it.
- Give each service its own database role with the privileges its own migration
  grants; do not hand out the PostgREST authenticator.
- Verify what the reader of a rule needs (a key name pattern that rejected an
  underscore blocked a legitimate secret name until we fixed it).

## 4. What the deploy agent does (read, not designed here)

A dispatcher run as the tenant's service user: a socket, one line per connection,
a closed list of verbs (`status`, `version`, `check`, `logs`, `build`, `deploy`,
`switch`, `restart`) and a content-addressed package. Extending it to a new
component (the DAGs) means adding to its closed lists, which is the case for the
[reusable agent kit](../tenant-app-on-fleet.md#9-known-gaps-in-mksrv).
