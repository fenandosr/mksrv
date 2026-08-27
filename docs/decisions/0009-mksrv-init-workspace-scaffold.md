# ADR 0009: `mksrv init` generates a workspace scaffold

- Status: Accepted
- Date: 2026-08-27
- Milestone: M1

## Context

M1 needs an entry point that produces a private operator workspace. The design
fork was whether `mksrv init` scaffolds a new workspace or requires one to
already exist. The operator chose scaffolding to lower first-use friction and
establish one blessed layout.

## Decision

`mksrv init` creates a new private workspace in the target directory (the
current directory, or `--workspace PATH`).

### Output

- `deployment.yaml` rendered from an embedded template. Required values are
  collected from flags (`--env`, `--region`, `--root-domain`, `--mgmt-cidr`,
  `--keycloak-domain`, `--headscale-domain`, `--acme-email`) or interactive
  prompts. `engine:` is stamped with the running binary's version.
- `tenants/` (empty, `.gitkeep`).
- `.gitignore` covering `.mksrv/`, `*.tfstate*`, `secrets.sops.yaml`, and
  `*.auto.tfvars.json`.
- `.mksrv/` state directory with a marker file.
- `README.md` stating the workspace belongs in a **separate private repository**,
  never the engine repo.

### Behavior

- Refuses to run if `deployment.yaml` already exists, unless `--force`; never
  silently overwrites an existing file.
- `--yes` skips prompts and fails if a required value is missing.
- `--json` emits `{"workspace": "<abs>", "created": ["deployment.yaml", ...]}`.
- On success it runs the standard validation pass and reports the result, so a
  fresh workspace is known to be schema- and semantics-valid before the operator
  edits it.
- The template set is embedded in the binary (a new `internal/scaffold` package
  with `embed`). It contains only synthetic placeholders; no real values enter
  the public repository.

## Consequences

- `init` is implemented after `internal/tf` (ADR 0008), since a scaffolded
  workspace is only useful once `plan`/`apply` can consume it.
- Prompt handling is the first interactive input path in the CLI; it honors
  `--yes` and a non-TTY stdin (fail rather than hang).
- The scaffold's `deployment.yaml` must stay in lockstep with
  `schemas/deployment.v1.json`; a test validates the rendered default against the
  embedded schema.
