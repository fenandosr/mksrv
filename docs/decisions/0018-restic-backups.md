# ADR 0018: `backup` stack — restic snapshots to S3

- Status: Accepted
- Date: 2026-09-03
- Milestone: M21

## Context

mksrv had no backups. Realm structure and per-tenant DBs are recreatable from the
workspace, but tenant *data* (once real) and any hand-made Keycloak users are
not. Before the distributed relaunch to production a real backup is required.

## Decision

A `backup` stack runs [restic](https://restic.net) on one host (the `appd` app
host), on a systemd timer, snapshotting to an S3 bucket.

- **What is captured** (all best-effort — one failure never skips the restic run):
  - `pg_dump -Fc` of every `db_<id>` and `app` from the Patroni primary;
  - `bao operator raft snapshot` of the OpenBao cluster;
  - Keycloak realm exports (`partial-export`, clients + groups + roles) per realm;
  - the dedicated stack volumes (`/var/lib/mksrv/vol`) and podman named volumes
    (Loki chunks, Grafana, pgAdmin).
- **Repository**: `s3:s3.<region>.amazonaws.com/mksrv-<env>-backups` — a
  deterministic bucket name, so the template needs no Terraform output. Terraform
  creates the bucket (versioned, SSE-S3, public access blocked, 30-day noncurrent
  expiry) only when a host carries `backup`, and grants that host's instance role
  `s3:{List,Get,Put,Delete}` on it (mirroring the OpenBao KMS pattern).
- **Credentials**: the restic repo password is a podman secret
  (`/mksrv/<env>/backup/restic_password`). S3 credentials come from the instance
  role over IMDS (`podman run --network host`), so **no access keys** anywhere.
- **OpenBao access**: `mksrv tenant apply` mints a *periodic* token bound to a
  `backup` policy that can only read `sys/storage/raft/snapshot` — never the root
  token.
- **Schedule**: `mksrv-backup.timer`, daily at 06:00 + up to 1 h jitter,
  `Persistent`. Retention `--keep-daily 7 --keep-weekly 4 --keep-monthly 6`.
- **CLI**: `mksrv backup run` (trigger + wait) and `mksrv backup list`. Restore is
  **documented, not automated** (`docs/backup.md`) — it is rare and needs
  judgement.

## Consequences

- `mksrv tenant apply` writes `/var/lib/mksrv/stacks/backup/backup.env` (repo
  URL, primary IP + super password, the raft token, the Keycloak admin password,
  realm/DB lists) at `0600` on the backup host.
- `terraform destroy` removes the bucket **only if empty** (no `force_destroy`) —
  a deliberate guard so a destroy never silently drops backups.
- Deferred: pgBackRest / WAL archiving for PITR; per-tenant restic repos;
  cross-region replication; a `mksrv backup restore` command.
