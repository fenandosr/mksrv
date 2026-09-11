# ADR 0032: Tenant mail hosting — the `mail` stack, for real

- Status: Accepted
- Date: 2026-09-11
- Milestone: M32
- Refines: the `mail` stack (M0 placeholder), `TenantMail` (M1)

## Context

`stacks/mail/stack.yaml` has described a `docker-mailserver`-backed mail stack
since M0, but it was never implemented — `templates: []`, a `README.md` that
says "added in the stack's implementation milestone." A host that lists `mail`
in `deployment.yaml` gets nothing. `tenants/<id>.yaml`'s `mail:` block
(`domains`, `inbound`, `dmarc_rua`) is real schema with a comment promising
"per-domain SES identity, DKIM, and MAIL FROM creation in the mail stack" — also
never wired to any code.

What exists and works is M25: **outbound-only** SES SMTP for the **operator's
own domain**, used for Keycloak's transactional email. It was deliberately
scoped away from tenant domains.

A tenant (MCPS) runs a real mail server today — inbound + outbound, ~30
mailboxes, `docker-mailserver` — on infrastructure outside mksrv, as part of
consolidating a legacy box onto the fleet. Building this properly closes both
gaps at once: the placeholder stack gets implemented, and a tenant's actual
mail workload gets a home on mksrv instead of ad hoc infrastructure.

This is the first mksrv feature that writes to a **tenant's own DNS zone**
values mksrv did not previously touch (MX, SPF, DMARC, DKIM) under the standing
rule "no tenant mail record changes without explicit sign-off." That sign-off is
scoped **per tenant, per adoption** — the rule stays in force for every tenant
that hasn't asked for this.

## Decision

### Shape: one shared server, tenants opt in

`mail` stays `per_tenant: false` — **one** `docker-mailserver` instance per
fleet, hosting mailboxes for every tenant that declares a `mail:` block, the
same shape as `database`/`cache`/`openbao` (shared engine, tenant-scoped
objects). It runs on the host carrying `base` (the edge) — ADR 0027 made that
the only host with a public IP, and inbound SMTP has no way around being
publicly reachable: the sending server on the other end has no VPN key to
present.

### `tenants/<id>.yaml`

```yaml
mail:
  domains: [acme.example.com]
  inbound: true
  dmarc_rua: dmarc@acme.example.com
  mailboxes:
    - address: gen@acme.example.com
      name: "Head of Bioinformatics"
    - address: admin@acme.example.com
```

- `domains` — every domain this tenant wants mailboxes/MX for. Requires
  `dns_override: {provider: route53, zone_id: …}`, same rule as `dns:`/`web:`.
- `inbound` — whether mksrv publishes an MX record. `false` keeps the domain
  send-only (SPF/DMARC still published, so outbound mail from this domain
  authenticates; nothing can deliver **to** it).
- `mailboxes` — declarative list, `{address, name?}`. `address`'s domain must be
  one of `domains`. No password field — generated.

### Passwords

`EnsureRandom` per mailbox, `/mksrv/{env}/mail/tenant_<id>_<local>_password` in
SSM (alnum, matching every other mksrv-generated secret), mirrored to
`kv/tenants/<id>/mail/<local>` for tenants that also consume `openbao` — same
pattern as the database/cache secret mirror. Delivery to the mailbox owner is
the operator's call (the `tenant secret-id`-style wrapped-delivery pattern
applies here too, manually, for now — a `mksrv tenant mail-password` command is
a natural follow-up, not required for this milestone).

### DNS: three different mechanisms, because the values are known at three
### different times

- **MX, SPF, DMARC** — fully computable from the tenant's own declared config
  (`mail.domains`, `mail.dmarc_rua`, the fixed shared hostname) with **no
  runtime dependency on the mail server**. Written by Terraform, extending the
  existing `tenant_dns` local in `infra/root/main.tf` (the same mechanism `dns:`
  and `web:` already use — `allow_overwrite = false`, so a tenant's pre-existing
  manual record blocks the apply instead of being clobbered):
  - `MX <domain> → 10 mail.<root_domain>` (only if `inbound: true`)
  - `TXT <domain> → "v=spf1 mx ~all"`
  - `TXT _dmarc.<domain> → "v=DMARC1; p=quarantine; rua=mailto:<dmarc_rua>"`
- **DKIM** — `docker-mailserver` generates its own OpenDKIM keypair per domain,
  on the container, the first time it's asked. The public half only exists
  *after* that happens — Terraform can't pre-compute it (contrast with M25's
  `aws_ses_domain_dkim`, a native AWS provider resource; there is no Route53-native
  equivalent for a self-hosted OpenDKIM key). `mksrv tenant apply`: execs
  `setup.sh config dkim domain '<domain>'` on the container if the key doesn't
  exist yet, reads the generated `.../mail.txt`, and **writes the TXT record
  directly via the AWS SDK** (`internal/aws` gains a small Route53
  `UpsertTXT`) into the tenant's own zone — the one genuinely new "mksrv
  writes tenant DNS from live infrastructure state, not from Terraform" path in
  the codebase. Idempotent: re-running with an existing key is a no-op besides
  reconfirming the record.
- **The shared server's own hostname and cert** stay entirely on the **operator**
  domain — `mail.<root_domain>` (e.g. `mail.cloud-it.click`), an A record to the
  edge EIP via the existing `dns_operator` module, no new tenant-touching
  surface. For the cert: rather than teach `docker-mailserver` its own ACME
  client (a new DNS-01 path, new IAM surface, new failure mode to operate), the
  `mail` stack adds an **empty Caddy fragment** for `mail.<root_domain>` — Caddy
  already runs on the edge, already owns `:80`, and already renews everything
  else on this domain via HTTP-01; one more hostname costs nothing new. `mksrv
  tenant apply` copies the resulting cert + key out of Caddy's storage into
  `/var/lib/mksrv/stacks/mail/tls/{fullchain,privkey}.pem` (`SSL_TYPE=manual`)
  and restarts `mailserver` only when they change — the same hash-compare-then-
  restart shape every other mksrv template write already uses. No new IAM
  grant, no new ACME client, no port-80 conflict.

Mail clients (Thunderbird, Outlook, phones) point at `mail.<root_domain>:993` /
`:587` regardless of which tenant's `@domain` the mailbox belongs to — the same
shape as any multi-domain hosted-mail provider. Recipients and senders only ever
see the tenant's own domain in headers/envelopes.

### Reconciliation (`mksrv tenant apply`)

For every tenant with a `mail:` block:

1. Ensure passwords exist (SSM, mirror to OpenBao).
2. Write `/var/lib/mksrv/stacks/mail/config/postfix-accounts.cf` (the full
   declared mailbox set across all tenants, `docker-mailserver`'s own
   config format — `docker-mailserver` reads it directly, no `setup.sh` calls
   in the steady state). Restart the container only if the rendered file
   changed (hash-compare, same pattern as every other mksrv template write).
3. Ensure the DKIM key exists per domain; publish/confirm its TXT record.

`mksrv apply --infra-only` (Terraform): MX/SPF/DMARC records, the operator A
record, the edge SG ports.

### Security group / firewall

`infra/modules/aws-host`: a new conditional ingress set, gated on
`local.is_edge && contains(var.stacks, "mail")` (mirrors the existing `web`
rule's shape) — TCP `25` (SMTP, inbound delivery), `465`/`587` (submission),
`993` (IMAPS), `0.0.0.0/0`. No `80`/`143`/`995` — HTTP-01 stays Caddy's, IMAP is
TLS-only, POP3 isn't offered.

### Storage

A dedicated `storage:` volume (`maildata`, ADR 0014 pattern) for
`/var/mail`+`/var/mail-state`+DKIM keys — durable, not lost on a container
recreate, and covered by the existing `backup` stack's restic run once the
`mail` host also carries `backup` (a config addition, not new code).

## Consequences

- **This is the first tenant-domain DNS mksrv writes outside `dns:`/`web:`
  (MX/SPF/DMARC/DKIM), and the first DNS write that comes from live
  infrastructure state instead of purely from Terraform.** Scoped to tenants
  that explicitly opt in with a `mail:` block, on a zone they already handed
  mksrv via `dns_override` — same boundary `dns:`/`web:` already operate inside.
- **One shared mail server is a single reputation.** A tenant that gets listed
  for spam affects deliverability for every tenant's domain sharing the server
  (SPF/DKIM/DMARC are per-domain and isolate *authentication*, not sending
  reputation on the shared IP). Acceptable at this scale; a noisy-neighbor
  problem would need per-tenant outbound rate limiting or, eventually, a
  dedicated server per tenant.
- **The edge takes on one more public service.** Consistent with ADR 0027/0028's
  existing trade-off (edge is the one public node); inbound SMTP is one more
  reason a full edge outage is felt broadly.
- **No self-service alias/quota/webmail.** Mailboxes are add/remove only for
  this milestone; aliases, per-mailbox quotas, and a webmail UI are deferred.
- **Migration is manual, not code.** Moving MCPS's ~30 existing mailboxes off
  their legacy box (message history, sent mail, existing DKIM selector rollover
  without a mail gap) is an operational runbook, not part of this ADR.
- Touchpoints: `schemas/tenant.v1.json` (`mail.mailboxes`), `internal/model`
  (`TenantMail.Mailboxes`, `TenantMailbox`), `internal/workspace/semantic.go`
  (`checkTenantMail`), `internal/aws` (Route53 `UpsertRecord`), a new
  `internal/cli/mail.go`, `stacks/mail/*` (real templates, storage, health,
  Caddy fragment for the cert), `infra/modules/aws-host` (SG ports),
  `infra/root/main.tf` (tenant MX/SPF/DMARC locals, operator A record).
