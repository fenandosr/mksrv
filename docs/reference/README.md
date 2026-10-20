# Reference

Information-oriented. Accurate, complete descriptions of the machinery —
consult these while working, rather than reading them end to end.

- [Workspace contract](workspace.md) — `deployment.yaml`, `tenants/*.yaml`,
  `tenants/*.users.yaml`, `mksrv.lock`, discovery precedence, generated data.
- [Stack catalog](stacks.md) — every `stacks/<name>/stack.yaml`, its kind,
  target, tenant scope, dependencies, and implementation state; derived tenant
  endpoints; Quadlet conventions.
- [DNS providers](dns-providers.md) — the desired-record model and what each
  provider (Route 53, manual, Cloudflare, RFC2136) supports today.
- [Capacity planning](capacity.md) — the RAM overcommit warning, the adaptive
  swap formula, per-service memory, and instance-type RAM tables.
- [Per-stack storage and retention](storage.md) — the `storage:` descriptor
  block, dedicated EBS volumes, the `retention:` block, and migrating a stack
  off a shared named volume.
- [Monitoring](monitoring.md) — what's scraped, bundled dashboards, the alert
  rule catalog, and wiring up notifications.
- [Tenant RBAC](rbac.md) — the four additive groups, what each grants across
  SSO/VPN/Postgres/OpenBao/Keycloak, and connecting directly to Postgres.
