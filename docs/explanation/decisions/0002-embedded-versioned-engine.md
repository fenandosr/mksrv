# ADR 0002: Embed and atomically extract a versioned engine

- Status: Accepted
- Date: 2026-08-27

## Context

The CLI must ship as one binary while Terraform and deployment templates require
real files. Running directly from a mutable source checkout would make applies
non-reproducible.

## Decision

Embed `infra/`, `stacks/`, and `schemas/` in the binary. Extract them to the user
cache under `mksrv/engine/<version>/` using a temporary directory and atomic
rename. Release versions reuse a completed extraction; `dev` refreshes it.
Workspace state and Terraform data never enter the engine cache.

## Consequences

A release contains an immutable engine and works without a source checkout.
Cache cleanup is safe. Executable mode must be restored explicitly for shell
hooks because embedded files do not preserve repository mode bits.
