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
  hosted: true
  spf_includes: [amazonses.com]
  dmarc_policy: reject
  dmarc_strict: true
  mailboxes:
    - address: admin@acme.example.com
      name: "Team Lead"
```

| field | |
|---|---|
| `domains` | every domain this tenant wants mailboxes/MX for. Requires `dns_override: {provider: route53, zone_id: …}`, same rule as `dns:`/`web:`. |
| `inbound` | `true` publishes an MX record. `false` keeps the domain send-only — SPF/DMARC still go out (so mail *from* this domain authenticates), nothing can deliver *to* it. |
| `dmarc_rua` | aggregate-report address in the published DMARC record. |
| `hosted` | the actual opt-in switch (default `false`). Without it, `domains`/`dmarc_rua` are just documentation — e.g. recording SPF/DMARC intent ahead of an actual migration — and mksrv writes nothing and provisions nothing. Only `hosted: true` makes `mksrv apply --infra-only` write DNS and `mksrv tenant apply` provision mailboxes/DKIM. (`mail` is a shared, non-per-tenant stack, so unlike `database` this can't be expressed by listing `mail` in the tenant's own `stacks:` — the schema rejects that.) |
| `spf_includes` | extra `include:` mechanisms folded into the computed SPF record, ahead of `mx` — for senders other than the shared mail server already authorized to send as this domain (e.g. a pre-existing SES sending identity). |
| `dmarc_policy` | the computed record's `p=` tag: `none` \| `quarantine` (default) \| `reject`. |
| `dmarc_strict` | `true` adds `adkim=s; aspf=s` (strict DKIM/SPF alignment) to the computed DMARC record. |
| `mailboxes` | declarative list, `{address, name?}`. `address`'s domain must be one of `domains`. No password field — mksrv generates one per mailbox. |

## Migrating real mailboxes from another mail server

docker-mailserver's `postfix-accounts.cf` format (`email|{SHA512-CRYPT}$6$...`)
is portable — a salted hash carries no per-installation secret. To migrate a
user without forcing a password reset, seed their existing hash directly
(copied verbatim from the old server's own `postfix-accounts.cf`) into SSM
*before* running `mksrv tenant apply`:

```bash
aws ssm put-parameter --type SecureString \
  --name "/mksrv/<env>/mail/tenant_<id>_<local_part>_password_hash" \
  --value '{SHA512-CRYPT}$6$...'
```

`<local_part>` is the address's local part, lowercased, with anything that
isn't `[a-z0-9]` collapsed to `_` (`alberto.zarza@…` → `alberto_zarza`) — same
rule `mksrv` uses for the generated-password ref, just a `_hash` suffix
instead. `mksrv tenant apply` checks for this ref *before* generating a
password; when present, it uses the hash as-is (nothing generated, nothing
else stored) — the user keeps logging in with the same password they always
had. Leave it unset for a genuinely new mailbox; mksrv generates one as usual.

If a domain already has its own SPF/DMARC (e.g. from a prior, non-mksrv mail
setup) and `hosted` is turned on, `mksrv apply --infra-only` will try to
**create** those two records and fail (`already exists`) since Terraform
doesn't know about them yet — `spf_includes`/`dmarc_policy`/`dmarc_strict` let
the computed value match (or deliberately improve on) what's already live.
One-time per record, the operator then tells Terraform it already exists —
doesn't touch the live DNS:

```bash
terraform import \
  'module.dns_tenant["<id>"].aws_route53_record.this["TXT <domain>"]' \
  <ZONE_ID>_<domain>_TXT
```

Requires a host in `deployment.yaml` carrying the `mail` stack (the edge, in
practice — it's the only public host, ADR 0027).

## What `mksrv tenant apply` does

For every tenant with `hosted: true` (skipped otherwise):

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

If `hosted: true`: writes MX (if `inbound: true`), SPF, and DMARC into your
zone — fully computed from the block above, no dependency on the server's
state. Otherwise, nothing.

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
