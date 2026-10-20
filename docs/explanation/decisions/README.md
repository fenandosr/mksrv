# Architecture decision records

Numbered, append-only decision records. A later ADR may supersede an earlier
one's conclusion (noted in both files) but the original is never rewritten.
See also [OPEN-QUESTIONS.md](OPEN-QUESTIONS.md) for questions deliberately left
unanswered.

- [ADR 0001: One desired-record model with provider-specific Terraform implementations](0001-dns-provider-abstraction.md)
- [ADR 0002: Embed and atomically extract a versioned engine](0002-embedded-versioned-engine.md)
- [ADR 0003: Deploy over SSH instead of introducing configuration management](0003-deploy-over-ssh.md)
- [ADR 0004: One Keycloak instance with one realm per tenant](0004-single-keycloak-instance.md)
- [ADR 0005: Use the Headscale tailnet as the service backplane](0005-mesh-as-backplane.md)
- [ADR 0006: Temporary dependency-free adapters for the M0 bootstrap](0006-m0-offline-bootstrap-adapters.md)
- [ADR 0007: Adopt the mandated CLI, YAML, and JSON Schema libraries](0007-adopt-mandated-cli-yaml-schema-libraries.md)
- [ADR 0008: Terraform execution wrapper and pinned version](0008-terraform-execution-wrapper.md)
- [ADR 0009: `mksrv init` generates a workspace scaffold](0009-mksrv-init-workspace-scaffold.md)
- [ADR 0010: Operator domain, per-tenant DNS, and a two-role host topology](0010-operator-domain-and-per-tenant-dns.md)
- [ADR 0011: Tenant-owned infrastructure — forwards, per-tenant DNS, mesh enrolment](0011-tenant-owned-infrastructure.md)
- [ADR 0012: `logs` and `security` stacks](0012-logs-and-security-stacks.md)
- [ADR 0013: `kind: cluster` stacks and the Patroni PostgreSQL cluster](0013-distributed-topology-and-postgres-cluster.md)
- [ADR 0014: Per-stack dedicated EBS volumes and configurable retention](0014-per-stack-storage-and-retention.md)
- [ADR 0015: OpenBao secrets stack (Raft HA, KMS auto-unseal)](0015-openbao-secrets-stack.md)
- [ADR 0016: Per-tenant RBAC groups (admin / dev / apps / vpn)](0016-tenant-rbac-groups.md)
- [ADR 0017: `database` runs on the Patroni cluster](0017-database-on-the-cluster.md)
- [ADR 0018: `backup` stack — restic snapshots to S3](0018-restic-backups.md)
- [ADR 0019: fleet-wide metrics (`agent` stack, central Prometheus scrape)](0019-fleet-metrics.md)
- [ADR 0020: per-service exporters (OpenBao, Postgres, Redis)](0020-service-exporters.md)
- [ADR 0021: edge visibility — blackbox probing, Keycloak, Caddy metrics](0021-blackbox-keycloak-caddy-metrics.md)
- [ADR 0022: alert-rule catalog, no auto-provisioned notifications](0022-alert-rules.md)
- [ADR 0023: per-tenant Keycloak login branding](0023-tenant-login-branding.md)
- [ADR 0025: operator SES SMTP for Keycloak transactional email](0025-operator-ses-smtp.md)
- [ADR 0026: Global Postgres RBAC roles](0026-global-postgres-rbac-roles.md)
- [ADR 0027: One public node — edge as NAT and bastion](0027-single-public-node-nat-bastion.md)
- [ADR 0028: Tenant public web endpoints on the edge (`web:` block)](0028-tenant-web-endpoints-on-the-edge.md)
- [ADR 0029: The tenant `database:` block — PostgREST opt-out, schema, extensions, and anon-by-default fix](0029-tenant-database-block.md)
- [ADR 0030: `web[].sso` — a Keycloak SSO gate at the edge](0030-web-sso-gate.md)
- [ADR 0032: Tenant mail hosting — the `mail` stack, for real](0032-tenant-mail-hosting.md)
- [ADR 0033: The shared mail server's certificate comes from acme.sh (DNS-01), not Caddy](0033-mail-cert-dns01-acme-sh.md)
