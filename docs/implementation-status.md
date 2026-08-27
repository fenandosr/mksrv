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

## Explicitly not implemented in M0

No AWS APIs, Terraform execution, provider generation, SSH connection, host
bootstrap, template rendering, Quadlet activation, DNS mutation, Keycloak or
Headscale API calls, secret decryption, tenant mutation, user mutation, deploy,
status, logs, or destroy behavior exists yet.

## M1 entry gate

Before infrastructure work begins, replace the temporary M0 dependency-free CLI,
YAML, and schema adapters with the mandated Cobra, `sigs.k8s.io/yaml`, and
`santhosh-tekuri/jsonschema/v6` packages, preserving the existing black-box tests.
See ADR 0006.
