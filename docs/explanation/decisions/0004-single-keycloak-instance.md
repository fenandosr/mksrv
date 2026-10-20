# ADR 0004: One Keycloak instance with one realm per tenant

- Status: Accepted
- Date: 2026-08-27

## Context

A Keycloak container per tenant increases memory use, upgrades, certificates,
and operational failure modes. Tenants still require isolated identity
namespaces, groups, users, clients, and branding.

## Decision

Run one Keycloak instance in the `identity` stack and create one realm per
tenant. Realm configuration is rendered/imported and then reconciled through the
Admin API. Administrative service-account credentials resolve from the private
secret store.

## Consequences

Identity operations are simpler and resource use is bounded. Keycloak is a
critical shared dependency, so backups, health checks, database durability, and
upgrade testing are mandatory. Realm-level isolation is configuration isolation,
not process isolation.
