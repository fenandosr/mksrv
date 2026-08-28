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

## Create a workspace

```bash
mksrv init ~/deploys/prod.workspace \
  --region us-east-1 --root-domain example.com \
  --mgmt-cidr 203.0.113.4/32 --acme-email ops@example.com
```

`init` renders `deployment.yaml`, `tenants/`, `.gitignore`, `.mksrv/`, and a
`README.md`, then validates the result. Omit a required flag on a terminal to be
prompted for it; pass `--yes` to require every value up front. Keep the workspace
in a **separate private repository**. Do not add secrets until SOPS is
configured.
