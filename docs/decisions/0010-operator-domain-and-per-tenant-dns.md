# ADR 0010: Operator domain, per-tenant DNS, and a two-role host topology

- Status: Accepted
- Date: 2026-08-27
- Milestone: M1–M5

## Context

A deployment can serve tenants whose `base_domain` values are independent apex
domains rather than subdomains of one root. `deployment.yaml` models a single
`dns.root_domain`, and semantic validation required every tenant `base_domain`
to sit inside it. The tenant schema already carries an unused `dns_override`
(`schemas/tenant.v1.json`), and `internal/workspace` already skips the
"within root" check when it is present.

Downstream questions were also open: how shared identity/mail endpoints are
named, how stacks are split across hosts, how secrets are stored, and the
minimum viable shape of `configd` (the broker consumed by `cloud-it-vpn`).

## Decision

### DNS: operator domain + per-tenant overrides

- `dns.root_domain` is a dedicated **operator domain** that hosts only shared
  endpoints: `auth.` (Keycloak), `vpn.` (Headscale), `cfg.` (configd), `mail.`
  (SES MAIL FROM), and a shared `grafana.`.
- A tenant on an independent domain sets
  `dns_override: { provider: route53, zone_id: <zone> }` and keeps its own apex
  `base_domain`. Derived service FQDNs (`db.`, `pgadmin.`, `grafana.`, …) are
  written into that tenant's hosted zone.
- No multi-zone fields are added to `deployment.yaml`. The Terraform root
  iterates tenants and invokes the DNS and SES modules once per tenant domain.

### Topology: an edge role and a data role

- The host carrying `base` is the **edge**: it holds the public EIP, terminates
  TLS, and is the only host with 80/443 open. `identity` and `mail` sit here too.
- Data-plane stacks (`database`, `monitor`, `files`, `analytics`) run on one or
  more **data** hosts with no public ingress. They join the tailnet outbound and
  reach identity over MagicDNS (ADR 0005).
- On a memory-constrained edge instance, cloud-init adds swap rather than forcing
  a larger instance type; the operator can still choose a larger type.

### Secrets: SSM Parameter Store + age

- Runtime secrets resolve from AWS SSM Parameter Store `SecureString` under the
  `/mksrv/<env>/...` paths already declared in the stack descriptors.
- The workspace-local `secrets.sops.yaml` is encrypted with `sops` + `age`.
- `internal/secrets` resolves both; `mksrv secrets set` writes to SSM.

### `configd`: minimal functional build

`configd` is a small service in this repository (`cmd/configd`,
`internal/configd`). Initially it validates a Keycloak OIDC access token,
resolves the tenant, mints a one-use Headscale pre-auth key, and returns the
Ed25519-signed compact-JWS `clientconfig` that `cloud-it-vpn` verifies. Policy
profiles and signing-key rotation are deferred.

## Consequences

- `stacks/identity/stack.yaml` must point `configd` at a real published image,
  replacing the `registry.invalid/...` placeholder.
- The edge instance role needs `route53:ChangeResourceRecordSets` scoped to the
  managed zones (ACME DNS-01) and `ses:SendRawEmail`.
- Keycloak's database is a dedicated Postgres in the `identity` stack, separate
  from the per-tenant `database` stack, with a scheduled dump to object storage.
- SES starts in sandbox; production access is a manual request during bring-up.
- Real domains, zone ids, account ids, and tenant lists live only in the private
  workspace repository, never here.
