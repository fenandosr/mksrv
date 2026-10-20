# Per-stack storage and retention

By default every stateful container writes to a named podman volume on the
single shared `/var/lib/mksrv` data volume. Stacks that need isolation or
provisioned IOPS declare a `storage:` block; the operator sets retention in
`deployment.yaml`.

## `storage:` in a stack descriptor

```yaml
storage:
  - name: pgdata        # host mount /var/lib/mksrv/vol/pgdata
    gb: 40
    iops: 4000          # gp3 provisioned; omit for the 3000 baseline
    throughput: 250     # gp3 MB/s; omit for the 125 baseline
    grows_with: metrics # data | metrics | logs — retention-driven sizing
    from: mksrv-monitor-prometheus   # legacy named volume, for `host migrate-volume`
    unit: mksrv-prometheus.service   # unit to stop during migration
```

The CLI aggregates every aws host's stacks' `storage:` entries, assigns guest
devices (`/dev/sd[g-p]`; `/dev/sdf` is the base data volume), and Terraform
attaches a dedicated encrypted gp3 `aws_ebs_volume` per entry. The bootstrap
matches each disk by its EBS volume id (the NVMe controller serial on Nitro),
formats it xfs if blank, and fstab-mounts it at `/var/lib/mksrv/vol/<name>`.
Templates reference the mount with `{{ .Volume "<name>" }}`.

Adopted by: `postgres` (`pgdata`, `raft`), `monitor` (`tsdb`), `logs` (`chunks`).

## Retention (`deployment.yaml`)

```yaml
retention:
  metrics_days: 15        # Prometheus --storage.tsdb.retention.time
  logs_days: 14           # Loki retention_period
  metrics_gb_per_day: 0.3 # assumed daily growth, for grows_with sizing
  logs_gb_per_day: 2
```

All optional (defaults shown). `grows_with: metrics` sizes the volume as
`base_gb + metrics_days * metrics_gb_per_day` (rounded up); `logs` likewise.

## Migrating an existing deployment

A fresh distributed install starts on dedicated volumes — nothing to migrate. To
move a running stack (`monitor` / `logs`) off its named volume:

```
mksrv apply                          # creates + attaches the EBS volume, mounts it
mksrv deploy <host> --stack <stack>  # re-renders the unit onto the bind mount
                                     # (container restarts on an empty dir)
mksrv host migrate-volume <host> <stack> [name...]
```

`migrate-volume` stops the unit, `rsync -aHAX --ignore-existing` the old
`_data` into the new mount (keeping the ~1-minute written since the redeploy),
restarts, and checks health. The old named volume is left as a backup —
`podman volume rm mksrv-<stack>-<x>` once you are satisfied.
