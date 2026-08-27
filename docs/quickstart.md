# M0 quickstart

## Build

```bash
make build
./bin/mksrv version
```

## Validate the synthetic workspace

```bash
./bin/mksrv validate examples/workspace
./bin/mksrv validate examples/workspace --json
```

A valid result reports two hosts, one tenant, two users, and seven embedded
catalog stacks.

## Validate a private workspace

```bash
export MKSRV_WORKSPACE=/path/to/private.workspace
mksrv validate
mksrv doctor
```

Discovery can also walk upward from a subdirectory. `--workspace` overrides the
environment for a single command.

## Create a workspace today

The interactive `mksrv init` command belongs to M1. During M0, copy
`examples/workspace/` outside the public repo, replace every synthetic value,
and keep the new directory private. Do not add secrets until SOPS is configured.
