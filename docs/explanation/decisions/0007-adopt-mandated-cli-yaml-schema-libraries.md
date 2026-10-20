# ADR 0007: Adopt the mandated CLI, YAML, and JSON Schema libraries

- Status: Accepted
- Date: 2026-08-27
- Supersedes: [ADR 0006](0006-m0-offline-bootstrap-adapters.md)

## Context

ADR 0006 introduced three temporary dependency-free adapters for command
parsing, the workspace YAML subset, and JSON Schema validation because the M0
build environment had no Go module access. It also defined the M1 entry gate:
replace all three adapters with the mandated upstream libraries while keeping the
existing black-box tests as the compatibility harness.

The module proxy is now reachable, so the gate can be closed before any M1
infrastructure work begins.

## Decision

Replace the adapters with the mandated libraries:

| Concern | Removed adapter | Adopted library |
|---|---|---|
| Command parsing | `internal/cli` hand-rolled flag parser | `github.com/spf13/cobra` |
| Workspace YAML | `internal/yamlmini` | `sigs.k8s.io/yaml` |
| JSON Schema | `internal/schema` hand-rolled validator | `github.com/santhosh-tekuri/jsonschema/v6` |

- `internal/yamlmini` is deleted. `internal/schema` and `internal/workspace`
  now call `sigs.k8s.io/yaml` directly (`YAMLToJSON` for the validation path,
  `Unmarshal` for `mksrv.lock`).
- `internal/schema` keeps its public surface (`Validator`, `New`,
  `Validate`, `ValidateYAML`, `Issue`). It compiles each embedded schema once
  with `AssertFormat` enabled and the default draft pinned to 2020-12, then maps
  `jsonschema.ValidationError` basic output onto `Issue{Path, Keyword, Message}`.
- `internal/cli` keeps `App`, `New`, `Execute`, `BuildInfo`, `ExitError`,
  `ExitCode`, and `AlreadyPrinted`. Cobra commands are constructed per
  `Execute` call so the type stays usable as a library. Global flags and the
  `--quiet` / `--verbose` conflict are enforced in a persistent pre-run hook;
  non-`ExitError` failures from Cobra map to exit code 2.

## Consequences

- The behavior contract is unchanged: `go test ./...` (including the CLI and
  workspace black-box suites) passes without modification, and
  `mksrv validate examples/workspace --json` still reports
  `{"valid": true, "issues": []}`.
- `sigs.k8s.io/yaml` accepts YAML features that `yamlmini` rejected outright
  (anchors, merge keys, block scalars). This is the intended upstream behavior;
  the workspace schemas still constrain the decoded shape.
- Schema issue `path` values are now derived from JSON Pointer instance
  locations rendered as `$`-rooted paths, and `keyword` is the trailing token of
  the failing keyword location. No test or documented output depends on the
  previous exact strings.
- New direct dependencies: `spf13/cobra`, `sigs.k8s.io/yaml`,
  `santhosh-tekuri/jsonschema/v6`; `go.sum` is regenerated. CI's Go matrix
  (1.23, 1.24) covers every dependency's minimum.
