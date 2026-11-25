# ADR 0046: Backup coverage of the hosts that hold state outside the Postgres cluster

- Status: Proposed (no code yet; each phase is its own change)
- Date: 2026-11-21
- Follows: [ADR 0018](0018-restic-backups.md) and its 2026-11-21 addendum

## Context

The daily restic job (ADR 0018) runs on **one host**, the one carrying the `backup` stack (`appd`), and sees only
that host's volumes. It dumps the cluster's databases, the OpenBao raft snapshot and the Keycloak realms. What lives
**outside** that reach, checked on the live fleet on 2026-11-21:

| Data on the edge | Where | Size seen | Why it matters |
|---|---|---|---|
| Keycloak's database: **every tenant's users, credentials and sessions** | Postgres container `mksrv-identity-postgres`, a podman volume | 69 MB | A realm export does not carry users, so this is the only copy |
| Headscale: nodes, keys, ACL state | SQLite with WAL (`db.sqlite`, `-wal`, `-shm`) plus `noise_private.key` | 4.3 MB | Losing it means re-enrolling every device of every tenant |
| Mailboxes | a dedicated EBS volume | 14 MB | Tenants' mail |
| Mail server configuration: account password hashes and the **DKIM private keys** | host file system under `stacks/mail/config` | small | DKIM keys are generated once on the server; losing them means new keys and a DNS change |
| Caddy data: certificates and the ACME account | podman volumes | small | Reissuable, but a rebuild can hit certificate rate limits |

Also verified:

- **No volume-level protection exists at all.** There is no EBS snapshot policy in the account (zero Data Lifecycle
  Manager policies); the three edge volumes (30 GB root, 20 GB data mounted at `/var/lib/mksrv`, 20 GB `maildata`) are
  encrypted gp3 volumes with no snapshots.
- A logical dump of Keycloak's database works: `pg_dump -Fc` through `podman exec` produced 315 KB.
- The IAM policy for the backups bucket already attaches to **any host that carries the `backup` stack**
  (`infra/modules/aws-host`), so the edge can be given access without new IAM design.
- `backup.env` is written for a single host (`f.backupHost()` returns the first one), and the script's database,
  OpenBao and Keycloak steps assume the host that runs the cluster dumps.
- All of it is small (under 100 MB of real data on the edge), so cost is not what limits this.

## Decision

Two layers, delivered in three phases, in this order.

### Layer 1 — volume snapshots, for everything, with no code on the hosts

A Terraform-managed AWS Data Lifecycle Manager policy snapshots, daily, every volume tagged
`mksrv:snapshot = daily`, with a short retention (proposal: 7 daily). Terraform tags the **data volumes** of every
host that holds state (the edge's data and `maildata`, the cluster nodes' raft and `pgdata`, `appd`'s); root volumes are
left out. Snapshots are incremental and keep the volume's encryption.

This is the cheapest way to stop the exposure: it needs no job, no credentials and no change on the edge, and it also
protects the cluster volumes that today rely on replication alone. Its limits are stated, not hidden: the snapshot is
**crash-consistent**, not application-consistent (a database recovers from it the way it recovers from a power cut),
and restoring one item means attaching the snapshot as a volume and reading from it.

### Layer 2 — logical backups, so one item can be restored

The existing job grows to run on every host that holds state, with its **components chosen from the stacks the host
carries**, not hard-coded:

- `appd`: what it does today (cluster dumps, OpenBao snapshot, Keycloak realms).
- `edge`: `pg_dump -Fc` of Keycloak's database, the mail configuration directory (account hashes, DKIM keys) and the mail
  data volume.

Rules that follow from sharing one restic repository: each host gets its own timer window and **`prune` runs on one host
only** (it takes an exclusive lock); raw podman volumes of a database are excluded from the file backup (a hot copy of a
Postgres data directory cannot be restored and would give false confidence); every component reports through the same
failure accounting as ADR 0018's addendum, labelled by host, so `BackupComponentFailed` names the edge component that
failed.

### Phases

1. **Layer 1.** The tag, the DLM policy and its role, and a documented restore of a volume from a snapshot, **performed
   once** on a scratch volume. Acceptance: snapshots exist for every tagged volume the next day, and the restore drill
   produced a readable volume.
2. **Layer 2 on the edge.** Per-host components and env, the edge's timer and IAM, the Keycloak database dump and the mail
   configuration. Acceptance: a snapshot with host `edge` contains a restorable `keycloak` dump (restored into a scratch
   database and queried) and the DKIM keys; a failed component marks the run failed and alerts.
3. **Headscale and drills.** A consistent copy of the SQLite database, and a written, tried restore for each component.

## Alternatives considered

- **Layer 2 only.** Needs code on the only public host and covers nothing until it is finished, while Layer 1 closes the
  exposure first with Terraform alone.
- **Layer 1 only.** Closes the exposure but leaves no way to restore one realm, one mailbox or one table without mounting
  a whole snapshot.
- **Run the job on every host, unchanged.** It would dump the cluster databases once per host.
- **Copy the SQLite files as they are.** A WAL database copied under write is not guaranteed consistent. Phase 3 picks
  between a brief stop of Headscale (seconds), SQLite's online backup from a throwaway container, or leaving Headscale
  to Layer 1.

## Consequences

- Daily snapshots add a small recurring cost. **Not measured**: the volumes hold little real data, so it should be cents to a
  few dollars a month; Phase 1 should record the first month's figure.
- The edge, the only public host, gains a scheduled job; it is isolated (a oneshot unit) and small.
- The retention of the two layers is independent and should be written down together.

## Not verified

How DLM behaves on tagged volumes (the tag conflict itself is fixed, see the addendum), concurrent restic runs from two hosts, the image and method for a SQLite online backup,
and restore times. None of the phases is started.

## Addendum (2026-11-22): the volume tag conflict was a prerequisite, and is fixed

Snapshot selection by tag needs tags that stay put. `aws_instance.volume_tags` tags every volume attached to the instance,
data volumes included, and `aws_ebs_volume` tags its own: every apply, each overwrote the other's `Name` and `mksrv:volume`
(nine in-place changes in every plan; some data volumes had the tag, others not). A snapshot tag would have been removed the
same way, with no error, leaving the policy with nothing to copy. The root volume is now tagged from `root_block_device`,
`volume_tags` is no longer set and is in `ignore_changes` (without that, removing it plans `volume_tags -> null`, which deletes
those tags from every attached volume until a second apply). The new plan against the live state is additive only: 5 instances
get root-volume tags and 4 data volumes get `mksrv:volume`, 0 removals; it converges to no changes after one apply. A test
pins the shape. Phase 1 can start once that apply is done.

## Phase 1 as built (2026-11-23)

**Three tiers, one DLM policy each** (`infra/root/main.tf`), daily at 09:00 UTC: `critical` keeps 30, `standard` 7, `low` 2. Each
volume carries `mksrv:snapshot = <tier>`, `mksrv:snapshot-rpo-hours` and `mksrv:snapshot-group = <env>-<tier>`; a policy targets
**only** the group tag (see the correction below). The tier comes from the catalog: a `priority:` on a stack (its data on the shared data volume) and on each
`storage:` volume, default `standard`; a host's data volume takes the highest priority among its stacks (`hostDataTiers`). On
today's fleet: critical = the edge's data volume and `maildata`, and `pgdata` and `baoraft` on the three core nodes (8 volumes);
standard = the data volumes of `appd` and the cores and `raft` (7); low = `tsdb` and `chunks` (2). Root volumes are never tagged.
`snapshot_tiers` (RPO and retention per tier), `snapshot_time_utc` and `snapshot_enabled` are root variables.

**The RPO is 24 h for every tier**, as decided for mail and users; the tiers differ in depth of history and in alert severity.
A weekly low tier was dropped: DLM intervals stop at 24 h (a weekly schedule needs `cron`, which I could not validate before an
apply), and the data it would save is a few hundred MB.

**Measure of goodness: coverage, not a ratio.** The ratio of critical to other snapshots is fixed by the inventory and improves
if snapshots are deleted, so it is not a goal. What can fail, and is measured, is `mksrv_snapshot_coverage_ratio{tier}`: the share of
a tier's volumes whose newest completed snapshot is within RPO plus 4 h of grace. `mksrv-snapshot-check.timer` on the `backup` host
computes it hourly (stdlib Python, SigV4-signed read-only calls with the instance role; `ec2:DescribeVolumes` and
`DescribeSnapshots`), and Prometheus alerts on it (`SnapshotCoverageCritical`, `SnapshotCoverageLow`, `SnapshotCheckFailing`,
`SnapshotCheckAbsent`) and on the case where nothing is tagged critical (`SnapshotNoCriticalVolumes`), which leaves no coverage
series for the others to fire on. Pending or failed snapshots do not count. Restore-tested volumes are not measured yet.

**Verified before any apply.** A read-only plan against the live state: 5 resources to add (three policies, role, attachment), 17
volumes retagged (8 critical, 7 standard, 2 low), 0 changes on instances, 0 removals; it also caught that DLM rejects a `:` or `(`
in a policy description. The check's SigV4 signature reproduces AWS's published test vector; its tests run the rendered script
against fake IMDS and EC2 servers and fail if a pending snapshot counts, the per-volume RPO is ignored, or an EC2 error is
swallowed. Run for real against EC2 (read-only, before any volume carried the tag) it read 22 volumes and 0 snapshots and wrote
`success 1`. The five alert expressions evaluate on the live Prometheus, and `unless on()` behaves as intended.

The same change adds `root_block_device[0].tags` to the instance's `ignore_changes`: the provider sets those tags at creation
but does not read them back, so every plan proposed them again (the earlier claim that one apply converged to no changes was
wrong).

**Not done yet:** the apply, the first snapshots, how the check reads real tier tags and snapshots (and runs on `appd` through
IMDS), whether the DLM role works with these encrypted volumes, and the restore drill that is this phase's acceptance
(`docs/how-to/volume-snapshots.md`, whose restore steps are untested until then).

## Correction (2026-11-24): DLM matches ANY target tag, and the first run showed it

The first scheduled run (2026-11-24 09:00 UTC) produced **66 snapshots of 22 volumes**, not 17 of 17. The cause was an assumption of
mine that a policy listing `mksrv:snapshot = <tier>` and `mksrv:env = <env>` as `target_tags` would take only volumes carrying both.
DLM takes volumes carrying **any** of them, so each of the three policies took every prod volume, the five root volumes included
(3 policies x 22 volumes = 66; 15 of them had no tier tag because root volumes carry none). The read-only plan could not show this: it
only shows the configuration, not how AWS interprets it.

What the run did confirm, and was untested before: **all 66 snapshots completed and are encrypted**, so the DLM role works with these
encrypted volumes, and the policies report no error. The fix is one target tag per policy, `mksrv:snapshot-group = <env>-<tier>`,
set by the module on the data volumes only, with a test that `target_tags` holds exactly one tag. The extra snapshots (the 15 of the
root volumes and the two off-tier copies of each data volume) are not removed by DLM once the policies stop selecting those volumes,
so they are deleted by hand; the restic job and every other snapshot are untouched.

## Phase 1 accepted: restore drill (2026-11-25)

The acceptance of phase 1 was met: a volume made from the newest snapshot of the edge's data volume was readable and its contents
recovered. On `appd`, a scratch Postgres with no network recovered Keycloak's data directory (automatic crash recovery, then ready),
and every realm's users and service accounts, credentials (18), clients (70) and groups (32) equalled the live database; Headscale's
database passed its integrity check with the live counts (20 nodes, 7 users); both DKIM private keys were valid and identical to
production's; the mail account count matched (49). The restore took under a minute end to end and the cleanup 24 s, leaving
nothing behind. The run of 2026-11-25 09:00 UTC also confirmed the fix above: 17 snapshots of 17 volumes, none of a root volume.

What this does **not** show: that the other volumes restore, that restored data can be put back into a running service, or what a
restore looks like when the host is gone. Phases 2 (logical backups of the edge) and 3 (Headscale and drills of each component)
remain.

