# ADR 0014: Per-stack dedicated EBS volumes and configurable retention

- Status: Accepted
- Date: 2026-09-02
- Milestone: M11

## Context

Every stateful container writes to a named podman volume under the shared
graphroot (`/var/lib/mksrv/containers/volumes/<name>/_data`) on the single data
volume — Postgres fsync contends with the Prometheus TSDB, and gp3 IOPS cannot be
provisioned per workload. Retention (`15d` Prometheus, `336h` Loki) is hardcoded
in templates. `stack.yaml`'s `resources.disk_gb` is dead metadata.

## Decision

### `storage:` block

A stack declares `storage: [{ name, gb, iops?, throughput?, grows_with?, from?,
unit? }]`. The CLI aggregates each aws host's stacks' entries (sizing
`grows_with: metrics|logs` from the retention policy), assigns `/dev/sd[g-p]`
(`/dev/sdf` stays the base data volume), and injects `host_volumes` into the
Terraform vars. `aws-host` attaches one encrypted gp3 `aws_ebs_volume` per entry
with `iops`/`throughput` only when set. The bootstrap (`BootstrapVersion` bump)
matches each disk by its EBS volume id via the NVMe controller serial
(`/sys/block/<dev>/device/serial`, no `nvme-cli`), formats xfs if blank, and
fstab-mounts it at `/var/lib/mksrv/vol/<name>`. Templates use
`{{ .Volume "<name>" }}`.

`postgres`, `monitor`, and `logs` adopt it.

### `retention:` in `deployment.yaml`

`{ metrics_days, logs_days, metrics_gb_per_day, logs_gb_per_day }`, all optional.
`render.Context.Retention` (a resolved value) feeds the Prometheus
`--storage.tsdb.retention.time` flag and the Loki `retention_period`, and drives
`grows_with` volume sizing.

### Migration, not auto-conversion

Switching a live stack from its named volume to a bind mount would start the
service on an empty dir. `mksrv host migrate-volume <host> <stack> [name]` does
the deliberate `stop → rsync --ignore-existing → start` after the operator has
run `apply` + `deploy --stack`. The old named volume is kept as a backup. Fresh
distributed installs skip all of this.

## Consequences

- `HostOutput` gains `data_volume_id` and `volumes`; `BootstrapParams` gains
  `DataVolumeID` and `Volumes`.
- The base data-volume mount in the bootstrap now prefers matching by volume id
  (falls back to the old "first blank disk" auto-detect for pre-existing hosts /
  old `outputs.json`).
- Deferred: dedicated volumes for OpenBao (when that stack lands),
  per-volume snapshot schedules, `st1`/`io2` volume types.
