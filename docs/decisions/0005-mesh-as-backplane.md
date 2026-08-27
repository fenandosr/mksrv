# ADR 0005: Use the Headscale tailnet as the service backplane

- Status: Accepted
- Date: 2026-08-27

## Context

Stacks can span AWS and existing hosts behind NAT. Exposing data-plane ports
publicly or building per-service VPN rules would be unsafe and difficult to
operate.

## Decision

Every non-identity host joins a Headscale-managed tailnet outbound using a
short-lived, one-use pre-authentication key. Cross-stack traffic uses mesh DNS
names and private tailnet ports. Public ingress is limited to the edge role.

## Consequences

Existing hosts need no public IP or inbound data-plane ports. Identity bootstrap
must be sequenced first and Headscale becomes critical infrastructure. Management
SSH remains explicit rather than being silently redirected through the mesh.
