# ADR 0006: Temporary dependency-free adapters for the M0 bootstrap

- Status: Temporary; must be superseded before M1 implementation
- Date: 2026-08-27

## Context

The implementation environment used to create M0 had Go 1.23 but no outbound
module access and no pre-populated module cache. The brief mandates Cobra,
`sigs.k8s.io/yaml`, and `santhosh-tekuri/jsonschema/v6`. Delivering uncompiled
source or unverifiable dependency stubs would make the milestone less useful.

## Decision

For M0 only, implement narrow internal adapters for command parsing, the
workspace YAML subset, and the JSON Schema vocabulary used by the four checked-in
schemas. Preserve package boundaries and black-box behavior so the mandated
libraries can replace these adapters without changing the workspace contract or
CLI tests.

## Guardrails

- Advanced YAML features are rejected explicitly rather than interpreted
  differently.
- The schema engine supports only vocabulary exercised by repository schemas.
- No third-party package identity is imitated or vendored incompletely.
- The M1 entry gate requires replacing all three adapters with the mandated
  upstream libraries and regenerating `go.sum` before AWS/Terraform work starts.

## Consequences

M0 builds and tests fully offline. It temporarily diverges from the preferred
technology choices, so it is not acceptable to begin M1 without superseding this
ADR. Existing tests are the compatibility harness for that migration.
