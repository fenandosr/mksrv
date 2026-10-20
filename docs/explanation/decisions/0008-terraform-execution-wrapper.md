# ADR 0008: Terraform execution wrapper and pinned version

- Status: Accepted
- Date: 2026-08-27
- Milestone: M1

## Context

M1 introduces real infrastructure operations (`init`, `plan --infra-only`,
`apply --infra-only`) that shell out to Terraform. Open question #6 asked which
Terraform version M1 pins. The engine must not perform an unbounded `latest`
lookup during `apply`, must produce reproducible behavior across operator
machines and CI, and must keep human-readable output on stderr while machine
output stays on stdout (the M0 I/O contract).

## Decision

### Libraries

Wrap Terraform through the official HashiCorp libraries rather than hand-rolled
`exec` calls:

- `github.com/hashicorp/terraform-exec/tfexec` — typed `init` / `validate` /
  `plan` / `apply` / `output` with structured errors and `-json` parsing.
- `github.com/hashicorp/hc-install` — version-pinned discovery and download of
  the Terraform binary, with checksum and signature verification.

These track the current releases (`terraform-exec v0.25.3`, `hc-install v0.9.5`).
`hc-install` older than v0.9.5 bundles a HashiCorp signing key whose ASCII-armor
export has expired, so step 4 below fails signature verification with those
versions. v0.9.5 requires `go 1.25`, which is why the module's Go baseline moved
to 1.25 (`go.mod`, `README`, `CONTRIBUTING`, and the CI matrix all state 1.25).

### Pinned Terraform version

`internal/tf.Version` is the single source of truth: **`1.9.8`**. It must match
the `terraform_version` pin in `.github/workflows/ci.yaml`; a divergence is a
CI-visible bug (a later `tf-version-sync` check will enforce it).

`1.9.8` is the version M0's CI already validated every module against, so M1
starts from a known-good baseline. Bumping is a deliberate, tested change.

### Binary resolution order (`internal/tf.Locate`)

1. `MKSRV_TERRAFORM` — an operator-supplied path; rejected unless it reports the
   pinned version exactly.
2. A binary previously downloaded into the mksrv cache
   (`$XDG_CACHE_HOME/mksrv/terraform/<version>/`), re-verified by version.
3. A matching Terraform already on `PATH` (via `hc-install` `fs` source).
4. Download the pinned release into the cache (`hc-install` `releases` source,
   with checksum + signature verification). Requires network; this is the only
   step that does.

No step ever selects a non-pinned version. Offline operation succeeds whenever
step 1, 2, or 3 resolves.

### Runner surface

`internal/tf.Runner` binds one working directory and streams Terraform's stdout
and stderr to the caller-provided log writer (mksrv's stderr). It exposes
`Init`, `Validate`, `Plan(planPath)`, `Apply(planPath)`, and `Output` returning
raw JSON values keyed by output name. `Raw()` returns the underlying
`*tfexec.Terraform` for operations later milestones need.

## Consequences

- New direct dependencies: `hashicorp/terraform-exec`, `hashicorp/hc-install`,
  `hashicorp/terraform-json`, `hashicorp/go-version`, plus their transitive tree
  (`go-cty`, `go-git`, `ProtonMail/go-crypto`, `go-multierror`, several
  `golang.org/x/*`). `go.sum` grows accordingly.
- The Go baseline moves from 1.23 to 1.25 (see above). The M0 brief's "Go 1.23 or
  newer" is superseded; Go 1.23 and 1.24 are EOL as of this milestone. The CI
  `go` matrix collapses to `1.25.x` and `staticcheck` moves to `v0.8.1` (the
  first release that analyzes Go 1.25).
- Terraform itself is downloaded at run time, never vendored or redistributed by
  mksrv, so its BUSL license does not attach to this Apache-2.0 repository.
  Whether to target OpenTofu instead remains an operator decision (open
  question, not blocking M1).
- Unit tests cover version pinning, cache-path derivation, the
  `MKSRV_TERRAFORM` version check (with a compiled fake binary), and the
  `terraform validate` diagnostic formatting. Full
  `init`/`plan`/`apply`/`output` behavior is covered by an `integration`-tagged
  test that CI's `terraform` job runs against a real pinned Terraform binary.
