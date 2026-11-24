# Volume snapshots (ADR 0046, phase 1)

Every data volume of the fleet is snapshotted once a day by one AWS Data Lifecycle Manager policy, defined in
`infra/root/main.tf` and applied with `mksrv apply --infra-only`. This covers what the restic job does not see:
the edge's identity database (every tenant's Keycloak users), Headscale, the mailboxes and the DKIM keys.

## What is covered

- **Selected by tag.** The `aws-host` module tags the **data volumes** with `mksrv:snapshot = daily`: the volume mounted at
  `/var/lib/mksrv` and every dedicated stack volume (Postgres, OpenBao raft, Prometheus, mail). The policy takes the
  volumes that carry both that tag and `mksrv:env = <env>`. A new host or stack volume is covered by being created.
- **Not covered.** Root volumes (they hold nothing that bootstrap does not rebuild).
- **Schedule and retention.** Daily at `snapshot_time_utc` (default 09:00 UTC, 03:00 in Mexico City) and the last
  `snapshot_retention_count` (default 7). Both are Terraform variables of the root module; `snapshot_enabled = false`
  removes the policy and the tag.
- **Crash-consistent, not application-consistent.** A snapshot is what a power cut would leave. Postgres and SQLite recover
  from that, but nothing flushes or locks before the snapshot. It is a safety net against losing a volume or a host, not a
  substitute for the logical backups (`docs/how-to/backup.md`), which can restore one database.

## See what exists

```sh
aws ec2 describe-snapshots --owner-ids self \
  --filters "Name=tag:mksrv:snapshot-by,Values=dlm" \
  --query 'Snapshots[].[StartTime,VolumeId,VolumeSize,State,Tags[?Key==`Name`]|[0].Value]' --output text | sort
```

Each volume should have one new snapshot a day and no more than the retention count. `copy_tags` is on, so a snapshot carries
its volume's `Name`, `mksrv:host` and `mksrv:volume`.

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

## Status of this procedure

**Not yet run.** The policy creates its first snapshots at the next scheduled time after the apply, so a restore drill
(phase 1's acceptance) can only be done after that. Until it has been done once, treat steps 2 to 5 as a plan, not as a
tested procedure, and record in ADR 0046 what the drill showed.

## Cost

Snapshots are incremental and store only written blocks. The tagged volumes total 287 GB provisioned, so the first set cannot
exceed 287 GB stored; the real figure is lower because most volumes are far from full, and the daily changes (mostly the
databases and the mailboxes) add to it for the 7 retained days. At the standard-tier price of about 0.05 USD per GB-month
(from memory, not checked against the current price list) the ceiling for the first set is about 14 USD a month. Read the
actual figure in Cost Explorer after the first month.
