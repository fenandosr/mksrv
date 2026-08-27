# ADR 0001: One desired-record model with provider-specific Terraform implementations

- Status: Accepted
- Date: 2026-08-27

## Context

Deployments may use Route 53, Cloudflare, RFC2136, or manual DNS. Stack and
tenant logic must not branch on provider-specific resource types.

## Decision

Compute a normalized list of A/CNAME records, then pass it to one DNS module.
Provider implementations are count-guarded and expose common `created` and
`pending` outputs. Manual mode creates no resources and returns the full desired
set. Per-tenant provider aliases are deferred to M6.

## Consequences

The CLI and stacks reason about records rather than providers. Terraform remains
responsible for managed DNS changes. Provider schema and credentials stay
isolated, while manual mode remains a supported first-class workflow.
