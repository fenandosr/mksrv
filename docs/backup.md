# Backups

The `backup` stack (ADR 0018) runs restic on the app host, daily.

```yaml
# deployment.yaml
hosts:
  appd: { provider: aws, ..., stacks: [database, cache, monitor, logs, security, backup] }
```

## What's in a snapshot

| Path in the snapshot | Source |
|---|---|
| `/stage/pg/db_<id>.dump`, `/stage/pg/app.dump` | `pg_dump -Fc` from the Patroni primary |
| `/stage/bao/raft.snap` | `bao operator raft snapshot` |
| `/stage/keycloak/<realm>.json` | Keycloak `partial-export` (clients + groups + roles) |
| `/vol/*` | dedicated stack EBS volumes (`monitor` tsdb, `logs` chunks, `openbao` raft, `postgres` pgdata) |
| `/podvol/*` | podman named volumes (Grafana, pgAdmin, …) |

Repository: `s3:s3.<region>.amazonaws.com/mksrv-<env>-backups` (versioned,
SSE-S3, private). Retention: 7 daily, 4 weekly, 6 monthly.

## Operating

```
mksrv backup run     # trigger now, wait, show the log
mksrv backup list    # restic snapshots --tag mksrv
```

The timer (`mksrv-backup.timer`, 06:00 + jitter) is enabled by `mksrv apply`.
`mksrv tenant apply` (re)writes `/var/lib/mksrv/stacks/backup/backup.env` with the
inputs the job needs.

## Restore

Restore is manual. From the backup host (`sudo -i`):

```sh
REPO=s3:s3.us-east-1.amazonaws.com/mksrv-prod-backups
r() { podman run --rm --network host \
  --secret mksrv-backup-restic_password,type=env,target=RESTIC_PASSWORD \
  -e RESTIC_REPOSITORY="$REPO" -v /restore:/restore docker.io/restic/restic:0.19.1 "$@"; }

mkdir -p /restore
r snapshots --tag mksrv
r restore <snapshot-id> --target /restore --include /stage
```

### A tenant database

```sh
# db_<id> already exists (from `mksrv tenant apply`); drop and recreate its schema,
# then load the dump as the tenant owner.
podman run --rm --network host -e PGPASSWORD="$SUPER" docker.io/library/postgres:16 \
  psql -h <primary-ip> -U postgres -d db_<id> -c 'DROP SCHEMA app CASCADE; CREATE SCHEMA app;'
podman run --rm --network host -i -e PGPASSWORD="$SUPER" -v /restore/stage/pg:/pg:ro \
  docker.io/library/postgres:16 \
  pg_restore -h <primary-ip> -U postgres -d db_<id> --no-owner --role=<id> /pg/db_<id>.dump
```

### OpenBao (lost quorum)

```sh
# on the intended leader, with a fresh single-node cluster up and unsealed:
podman run --rm --network host -e BAO_ADDR=http://<node-ip>:8200 -e BAO_TOKEN=<root> \
  -v /restore/stage/bao:/bao:ro ghcr.io/openbao/openbao:2.6.1 \
  bao operator raft snapshot restore -force /bao/raft.snap
```

### A Keycloak realm

Delete the realm, then import `/restore/stage/keycloak/<realm>.json` via the
Admin console (Realm settings → Partial import) or
`POST /admin/realms/<realm>/partialImport`.

## Deferred

pgBackRest / WAL archiving (PITR), per-tenant repos, cross-region copy, a
`mksrv backup restore` command.
