# Volume snapshots (ADR 0046, phase 1)

Every data volume of the fleet is snapshotted once a day by AWS Data Lifecycle Manager, at a depth that depends on how much
its data matters. The policies live in `infra/root/main.tf` and are applied with `mksrv apply --infra-only`. This covers what
the restic job does not see: the edge's identity database (every tenant's Keycloak users), Headscale, the mailboxes and the
DKIM keys. A check on the backup host reports whether it is actually working.

## Three tiers

The tier of a volume is declared in the stack catalog, not in `deployment.yaml`: a stack's `priority:` is the priority of its
own data on the host's shared data volume, and each `storage:` volume has its own `priority:`. The default is `standard`.
A host's data volume (the one mounted at `/var/lib/mksrv`) takes the highest priority among its stacks.

| Tier | Meaning | Kept | On today's fleet |
|---|---|---|---|
| `critical` | cannot be rebuilt: users, mail, secrets, tenant databases | 30 daily snapshots | edge data volume (identity, mail config, DKIM keys), `maildata`, `pgdata` and `baoraft` on the three core nodes |
| `standard` | host configuration and the rest | 7 daily | data volume of `appd` and of the core nodes, `raft` |
| `low` | regenerates | 2 daily | `tsdb` (metrics), `chunks` (logs) |

- **RPO is 24 h in every tier** (the snapshot interval); the tiers differ in how far back you can go. `snapshot_tiers` in the root
  module holds `rpo_hours` and `retain` per tier, and `snapshot_time_utc` (default 09:00 UTC, 03:00 in Mexico City) the start time.
  `snapshot_enabled = false` removes the policies and the tags.
- **Selected by tag.** The `aws-host` module tags each data volume `mksrv:snapshot = <tier>`, `mksrv:snapshot-rpo-hours` and
  `mksrv:snapshot-group = <env>-<tier>`; each tier's policy targets **only** that last tag. A new host or stack volume is covered by
  being created. Root volumes are never tagged: they hold nothing that bootstrap does not rebuild.
- **DLM matches ANY target tag, not all.** A policy that listed two (the tier and `mksrv:env`) took every volume of the environment,
  root volumes included, once per policy: 66 snapshots of 22 volumes on the first run. That is why a policy has exactly one target
  tag, whose value already carries the environment and the tier, and a test pins it.
- **Crash-consistent, not application-consistent.** A snapshot is what a power cut would leave. Postgres and SQLite recover
  from that, but nothing flushes or locks before the snapshot. It is a safety net against losing a volume or a host, not a
  substitute for the logical backups (`docs/how-to/backup.md`), which can restore one database.

## Is it working? The check

`mksrv-snapshot-check.timer` runs hourly on the host that carries `backup` and writes `snapshots.prom` for Prometheus. It asks
EC2 (read-only: `ec2:DescribeVolumes`, `ec2:DescribeSnapshots`) for the newest **completed** snapshot of every tagged volume;
a pending or failed snapshot protects nothing and does not count. A volume is fresh when that snapshot is younger than its RPO
plus 4 hours of grace.

| Metric | Meaning |
|---|---|
| `mksrv_snapshot_coverage_ratio{tier}` | fresh volumes / volumes of the tier. **1 is good**; this is the measure of goodness per tier |
| `mksrv_snapshot_newest_timestamp_seconds{tier,volume,host,volume_id}` | start of the newest completed snapshot, 0 when there is none |
| `mksrv_snapshot_volumes{tier}` | how many volumes the tier covers |
| `mksrv_snapshot_check_success`, `..._last_run_seconds` | whether EC2 could be asked, and when |

| Alert | When | Severity |
|---|---|---|
| `SnapshotCoverageCritical` | critical coverage below 1 for 2 h | critical |
| `SnapshotCoverageLow` | standard or low coverage below 1 for 6 h | warning |
| `SnapshotNoCriticalVolumes` | EC2 answers but no volume is tagged critical (a policy or tag that selects nothing leaves no coverage series, so this is the case the others cannot see) | critical |
| `SnapshotCheckFailing` / `SnapshotCheckAbsent` | the check fails, stopped, or never ran | warning |

The raw ratio of critical to other snapshots is deliberately not a goal: it is fixed by what the data is, and could be "improved"
by deleting snapshots. Coverage against the RPO is what can fail.

## See what exists

```sh
aws ec2 describe-snapshots --owner-ids self \
  --filters "Name=tag:mksrv:snapshot-by,Values=dlm" \
  --query 'Snapshots[].[StartTime,VolumeId,VolumeSize,State,Tags[?Key==`Name`]|[0].Value,Tags[?Key==`mksrv:snapshot`]|[0].Value]' --output text | sort
```

`copy_tags` is on, so a snapshot carries its volume's `Name`, `mksrv:host`, `mksrv:volume` and tier.

## Restore one

The volume is XFS. Restoring means making a **new** volume from the snapshot and reading from it; do not replace a live volume
without stopping what uses it.

1. Pick the snapshot and the availability zone of the instance that will read it:

   ```sh
   aws ec2 describe-snapshots --owner-ids self --snapshot-ids snap-... --query 'Snapshots[0].[VolumeId,StartTime,State]'
   aws ec2 describe-instances --instance-ids i-... --query 'Reservations[0].Instances[0].Placement.AvailabilityZone'
   ```
2. Create the volume (it stays encrypted, as the snapshot is) and attach it at a free device:

   ```sh
   aws ec2 create-volume --snapshot-id snap-... --availability-zone <az> --volume-type gp3 \
     --tag-specifications 'ResourceType=volume,Tags=[{Key=Name,Value=restore-scratch}]'
   aws ec2 attach-volume --volume-id vol-... --instance-id i-... --device /dev/sdz
   ```
3. On the instance, mount it **read-only and ignoring the UUID**: the restored XFS has the same UUID as the live volume and
   would otherwise be refused.

   ```sh
   sudo lsblk                                  # find the new device
   sudo mkdir -p /mnt/restore
   sudo mount -o ro,nouuid /dev/<device> /mnt/restore
   ```
4. Copy out what you need. For a database, copy the data directory to a scratch location and start a scratch container on
   the copy; never point a running database at the mounted snapshot.
5. Clean up: `sudo umount /mnt/restore`, `aws ec2 detach-volume`, `aws ec2 delete-volume`.

## Status

**The restore steps are untested.** The policies create their first snapshots at the next scheduled time after the apply, so a
restore drill (phase 1's acceptance) can only be done after that. Until it has been done once, treat steps 2 to 5 as a plan,
not as a tested procedure, and record in ADR 0046 what the drill showed. The check itself was run against the real EC2 API
before any volume was tagged (22 volumes read, 0 snapshots, success); how it reads real tier tags and real snapshots is not
verified until after the apply.

## Cost

Snapshots are incremental and store only written blocks. The 17 tagged volumes total 287 GB provisioned but hold about 15 GB of
data (measured with `df` on the edge, `appd` and `core1`; the other two core nodes are assumed to match), so the first set is
about that size, not 287 GB, and the daily changes (mostly the databases and the mailboxes) add to it over the retention. At the
standard-tier price of about 0.05 USD per GB-month (from memory, not checked against the current price list) that is around
1 USD a month or less; the ceiling if every provisioned block were written is about 14 USD. Read the real figure in Cost
Explorer after the first month.
