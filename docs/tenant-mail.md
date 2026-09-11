# Tenant mail hosting (`mail:`)

A tenant can get real inbound + outbound mailboxes on its own domain, hosted on
the fleet's shared `docker-mailserver` (ADR 0032). Different from
`mail.outbound_smtp` (`docs/mail-smtp.md`), which is send-only, operator-domain,
for Keycloak's own email.

```yaml
# tenants/<id>.yaml
mail:
  domains: [acme.example.com]
  inbound: true
  dmarc_rua: dmarc@acme.example.com
  mailboxes:
    - address: admin@acme.example.com
      name: "Team Lead"
```

| field | |
|---|---|
| `domains` | every domain this tenant wants mailboxes/MX for. Requires `dns_override: {provider: route53, zone_id: …}`, same rule as `dns:`/`web:`. |
| `inbound` | `true` publishes an MX record. `false` keeps the domain send-only — SPF/DMARC still go out (so mail *from* this domain authenticates), nothing can deliver *to* it. |
| `dmarc_rua` | aggregate-report address in the published DMARC record. |
| `mailboxes` | declarative list, `{address, name?}`. `address`'s domain must be one of `domains`. No password field — mksrv generates one per mailbox. |

Requires a host in `deployment.yaml` carrying the `mail` stack (the edge, in
practice — it's the only public host, ADR 0027).

## What `mksrv tenant apply` does

1. Generates each mailbox's password (`EnsureRandom`, SSM
   `/mksrv/<env>/mail/tenant_<id>_<local>_password`) and writes the shared
   `postfix-accounts.cf`. Restarts the mail server only if it changed —
   aggregated across **every** mail-consuming tenant, not just the one you
   named, so a partial `tenant apply <id>` never drops another tenant's
   mailboxes.
2. Copies the mail server's TLS cert in from Caddy (which already holds one for
   `mail.<root_domain>` — nothing new to issue or renew).
3. Generates (once) and publishes the DKIM key for each of your domains —
   the one DNS write mksrv makes directly via the AWS SDK instead of Terraform,
   because the key only exists after `docker-mailserver` creates it.

## What `mksrv apply --infra-only` does

Writes MX (if `inbound: true`), SPF, and DMARC into your zone — fully computed
from the block above, no dependency on the server's state.

## Client settings

Point Thunderbird / Outlook / your phone's mail app at the **shared hostname**,
not your own domain — `mail.<root_domain>` (ask your operator), port `993`
IMAPS / `587` submission (STARTTLS) or `465` (implicit TLS). Your address is
still `you@acme.example.com`; the server hostname is shared across every tenant
hosted here, the same shape as any multi-domain hosted-mail provider.

## Not yet

Aliases, per-mailbox quotas, a webmail UI, and self-service password delivery
(today the operator reads the SSM password and hands it to you) are deferred —
see ADR 0032's consequences.
