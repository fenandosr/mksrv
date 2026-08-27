# Changelog

## Unreleased — M1

- Added `internal/tf`: pinned Terraform version (`1.9.8`), binary location and
  download via `hashicorp/hc-install`, and a `Runner` over
  `hashicorp/terraform-exec` (`Init`, `Validate`, `Plan`, `Apply`, `Output`).
  ADR 0008.
- `mksrv version` now reports the real pinned Terraform version.
- Recorded ADR 0009: `mksrv init` will generate a private workspace scaffold.
- Raised the Go baseline to 1.25 (required by current `hc-install`); CI matrix is
  now `1.25.x` and `staticcheck` is `v0.8.1`.

## Unreleased — M0

- Added public/private engine-workspace boundary and repository scaffolding.
- Added embedded engine cache and seven stack descriptors.
- Added four JSON Schemas and schema-plus-semantic workspace validation.
- Added `version`, `validate`, and `doctor` with JSON output.
- Added synthetic examples, tests, release automation, and public-data hygiene.
- Replaced the temporary offline adapters with Cobra, `sigs.k8s.io/yaml`, and
  `santhosh-tekuri/jsonschema/v6`; behavioral tests unchanged (ADR 0007).
- Embedded `time/tzdata` so `-trimpath` builds validate timezones without
  system zoneinfo.
