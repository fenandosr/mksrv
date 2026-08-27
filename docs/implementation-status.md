# Implementation status

## M0 — implemented

- Repository, license, Makefile, GoReleaser, CI/release workflows.
- Embedded engine tree and atomic versioned cache extraction.
- Four draft 2020-12 JSON Schemas.
- Models for deployment, hosts, tenants, users, stacks, and lock files.
- Workspace discovery, loading, schema checks, and semantic checks.
- Functional `version`, `validate`, and `doctor` commands with JSON output.
- Synthetic workspace and automated tests.
- Terraform/module and stack/template directory scaffolding.
- Public-data hygiene script and gitleaks configuration.
- CLI on Cobra, YAML via `sigs.k8s.io/yaml`, schema validation via
  `santhosh-tekuri/jsonschema/v6` (ADR 0007); embedded `time/tzdata`.

## Explicitly not implemented in M0

No AWS APIs, Terraform execution, provider generation, SSH connection, host
bootstrap, template rendering, Quadlet activation, DNS mutation, Keycloak or
Headscale API calls, secret decryption, tenant mutation, user mutation, deploy,
status, logs, or destroy behavior exists yet.

## M1 entry gate

Closed on 2026-08-27. The temporary M0 dependency-free CLI, YAML, and schema
adapters were replaced with Cobra, `sigs.k8s.io/yaml`, and
`santhosh-tekuri/jsonschema/v6`; the existing black-box tests passed unchanged.
See ADR 0007 (which supersedes ADR 0006). M1 infrastructure work may now begin.
