# ADR 0048: `mksrv tenant contract` — what a tenant's app sees, from the code that builds it

- Status: Accepted
- Date: 2026-11-28
- Refines: ADR 0029, ADR 0039, ADR 0041

## Context

A local analogue of the fleet's services (database roles, OpenBao paths and
policies, Keycloak realm, Redis ACL, PostgREST) is being built outside mksrv, so
that an application meets its production contract on a laptop. Everything it
needs is derived from the tenant's YAML by Go functions that only
`tenant apply` calls. Copied by hand, it drifts silently. `validate --json`
reports only issues, and `tenant list --json` only ids.

## Decision

`mksrv tenant contract <id>` prints one JSON document with the tenant's database
SQL, OpenBao policies, AppRoles, OIDC and Transit arguments, the field names of
the secrets mksrv writes, the Keycloak realm, clients and mappers, the Redis ACL
and configuration, the PostgREST unit, the S3 bucket declaration and the
declared images.

- **The same functions as `tenant apply`.** Where a value was built inline (the
  realm and client representations, the two unbound AppRoles, the OIDC group
  roles, the Transit key), it moved to a function both call.
- **Offline.** It reads the validated workspace and the embedded engine. No
  infrastructure outputs, no cloud, no SSH, no secret store; it writes nothing,
  including `mksrv.lock`.
- **No secrets.** Passwords and client secrets are fixed placeholders
  (`__PASSWORD__`, `__AUTH_PASSWORD__`, `__PASSWORD_<role>__`, `__SECRET__`).
- **Versioned.** `contract_version` changes when a field is removed or changes
  meaning. Adding a field does not change it.

## Consequences

- The output describes what the workspace and this binary **declare**. It says
  nothing about what is deployed: a tenant not yet applied, an older binary on
  the operator's side or a newer patch of a floating image tag all differ.
- The contract is the tenant's own, but it carries deployment names (the
  Keycloak domain, the tenant's CIDRs and bucket). It is workspace data: whoever
  runs the command decides who receives it.
- It needs a workspace. A developer without one receives the file from the
  operator.
- The S3 IAM policy lives in Terraform and Keycloak's default client scopes in
  its image; the output lists both under `not_covered`.
- Changing one of the shared builders now changes a published contract.

## Alternatives

- **Document the contract by hand.** The drift the command exists to prevent.
- **Export the Go packages for the kit to import.** Ties the kit to mksrv's
  internal API and to Go.
- **Read the live fleet.** Needs operator credentials, for an answer a developer
  should get without them.

## Not verified

That a local analogue built from this output behaves like the fleet. The database
scripts were applied twice to a throwaway Postgres 16 for five real tenants;
the OpenBao, Keycloak, Redis and PostgREST parts have not been applied anywhere.
