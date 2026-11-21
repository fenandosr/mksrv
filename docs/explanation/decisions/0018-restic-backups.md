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
  **documented, not automated** (`docs/how-to/backup.md`) — it is rare and needs
  judgement.

## Consequences

- `mksrv tenant apply` writes `/var/lib/mksrv/stacks/backup/backup.env` (repo
  URL, primary IP + super password, the raft token, the Keycloak admin password,
  realm/DB lists) at `0600` on the backup host.
- `terraform destroy` removes the bucket **only if empty** (no `force_destroy`) —
  a deliberate guard so a destroy never silently drops backups.
- Deferred: pgBackRest / WAL archiving for PITR; per-tenant restic repos;
  cross-region replication; a `mksrv backup restore` command.

## Addendum (2026-11-21): the snapshot token is reused, not re-minted

`mksrv tenant apply` used to mint a new periodic token on every run and never revoked the old ones: twenty
were live when it was noticed. That churn was, by accident, the only renewal the token had: the `backup`
policy grants no `renew-self`, so a periodic token that nobody renews dies after its period (30 days) and the
snapshots would start failing without a message.

Now the token is kept in SSM (`/mksrv/<env>/openbao/backup_token`) and **renewed with the root token on each
run** (it travels on stdin). A new token is minted only when the stored one is missing or no longer renews;
at that moment every other token whose only policy is `backup` is revoked (a token with any other policy,
root included, is never touched). Still true, and not addressed here: if no `tenant apply` runs for 30 days
the token expires; the renewal is performed by `apply`, not by the backup job.

## Addendum (2026-11-21): a backup that cannot collect a component now fails

Looking at a run for another reason showed that, in **every** run since at least 2026-11-12, the OpenBao raft
snapshot and every Keycloak realm export failed, and the job still ended with `done`, wrote the freshness metric
and let `BackupStale` stay quiet. Two independent causes, both checked against the live services:

- **Keycloak:** the script called `partial-export` with a GET; the Admin API answers 404 to it and 200 to a POST.
  The export could never have worked.
- **OpenBao:** the snapshot call through the HTTP API with the backup token answers 200 (the token and the policy
  were right); what failed was `bao operator raft snapshot save -`, whose argument is a file path, not stdout. The
  snapshot is now fetched with `curl` from `/v1/sys/storage/raft/snapshot`.

The silence mattered more than either bug, so the job now:

- counts each failed collection step (`fail`), prints why and the first lines of that step's error file (the file is
  deleted at the end of the run, so the journal is the only place the reason survives);
- still writes the restic snapshot (an incomplete snapshot beats none) but then **exits 1**, so the systemd unit shows
  failed and `mksrv backup run` reports it;
- moves `mksrv_backup_last_success_seconds` only when every component was collected, and **keeps its previous value**
  after a failed run (if the series vanished, `BackupStale`, which compares it to now, would have nothing to compare);
- exports `mksrv_backup_failures` and `mksrv_backup_component_ok{component="postgres|openbao|keycloak"}`, and a new
  alert `BackupComponentFailed` fires when a component is 0.

The tests execute the rendered script with `podman` and `curl` replaced by stubs that behave like the services (Keycloak
answers 404 to a GET): a test that only reads the script's text could not have seen either bug. Mutating the script
(dropping `-X POST`, dropping the final `exit 1`) makes them fail.

**Still not covered, and not addressed here:** the job runs only on the host that carries `backup` and sees only that
host's volumes. The `edge`'s Keycloak database (every tenant's users and credentials), its Headscale state and the
mail data are outside it; a realm export would not have carried users anyway. See ADR 0046.

