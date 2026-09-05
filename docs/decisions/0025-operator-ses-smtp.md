# ADR 0025: operator SES SMTP for Keycloak transactional email

- Status: Accepted
- Date: 2026-09-05
- Milestone: M25

## Context

mksrv-created Keycloak users get no password (`ensureUser` sets no
`credentials`) — the only way for someone to get in is a manual admin reset
or Keycloak's self-service "forgot password" flow, and the latter needs a
realm with SMTP configured. No realm had one.

## Decision

- **Scope is deliberately narrow**: this is not the `mail` stack (still an
  unimplemented M5 descriptor — inbound mailboxes, per-tenant domains, a real
  mail server). It is exactly enough outbound SMTP for Keycloak's own
  password-reset / email-verification mail.
- **One sender, the operator root domain**: `noreply@<root_domain>`, shared
  by every tenant realm. Never a tenant's own domain — this stays inside the
  standing "no tenant mail DNS" constraint because it only ever touches the
  operator zone mksrv already manages (the same zone `auth.`/`vpn.`/`cfg.`
  live in).
- **Opt-in**: `mail.outbound_smtp` (default `false`). Off by default because
  it creates a billable-adjacent (SES is nearly free, but it's still a new
  AWS identity + IAM user) resource and, more importantly, because a new SES
  account starts in the **sandbox** — sending is capped to individually
  verified recipient addresses until AWS grants production access (a manual
  support-console request; Terraform/mksrv cannot do this). Turning the flag
  on doesn't make outbound mail actually work end-to-end by itself.
- **Custom MAIL FROM domain** (`mail.<root_domain>`, one extra MX + TXT/SPF):
  chosen over the SES-default `*.amazonses.com` MAIL FROM for better DMARC
  alignment — the operator explicitly asked for this over the faster
  DKIM-only setup.
- **Credential shape**: a dedicated IAM user scoped to `ses:SendRawEmail`
  only (no console access, no other permissions), one access key. SMTP has
  no notion of access-key/secret-key pairs — only a username/password — so
  the secret access key is converted via AWS's published SigV4-based
  derivation (`internal/aws.DeriveSESSMTPPassword`): a fixed literal date
  string, "aws4_request", "SendRawEmail", and a version byte, all exactly as
  AWS's own reference implementation defines them. **Not verified against a
  live SES SMTP endpoint in this session** — spot-check by actually
  authenticating once before relying on it.
- **Where the credential lives**: Terraform necessarily holds the IAM access
  key (it's the resource's source of truth), exposed via a `sensitive`
  output. mksrv immediately mirrors the *derived* SMTP username/password into
  SSM (`tenantSMTPSpec`, unconditional `Put`, not `EnsureRandom` — Terraform
  is the source of truth here, not mksrv, so a rotated key must always win)
  and everything downstream (Keycloak's realm SMTP settings) reads from
  there, the same as every other mksrv application secret.
- **TXT values are plain text, not manually quoted.** `aws_route53_record`
  only wants literal `""` to *concatenate* segments of a value longer than
  255 characters (its own docs); adding it around a short value (the SPF
  string, the SES verification token) sends Route53 a doubly-quoted string
  and fails apply with "InvalidCharacterString (Value should be enclosed in
  quotation marks)" — hit live, on the very first real `apply` with the flag
  on.
- **DKIM's 3 CNAME records are `count`-based, not part of the shared `dns`
  module's `for_each`**: SES generates the DKIM tokens when
  `aws_ses_domain_dkim` is created, so the token — and therefore the CNAME's
  *name*, not just its value — is unknown until apply. The `dns` module
  builds its `for_each` map key from `"${type} ${fqdn}"`, and Terraform
  requires every `for_each` key to be known at plan time; an unknown fqdn in
  that list poisons the whole map, not just the DKIM entries (hit live:
  "Invalid for_each argument ... will be known only after apply"). `count = 3`
  sidesteps it — the *count* (always exactly 3 for Easy DKIM) is known even
  though `dkim_tokens[count.index]` isn't, and `count`, unlike `for_each`,
  never needs its index set known ahead of time. Route53-only for now — DKIM
  auto-creation isn't wired for Cloudflare/RFC2136.
- **Reconciling onto realms**: `RealmSpec.SMTP`, applied unconditionally
  every `tenant apply` run when set, no diffing — Keycloak masks the password
  on `GET` (`**********`), so there is nothing meaningful to compare against,
  and re-`PUT`ting identical settings is a no-op in effect.

## Consequences

- No tenant DNS touched. One IAM user, one SES identity, 5 new operator-zone
  DNS records (1 verification TXT + 3 DKIM CNAME + 1 MAIL FROM MX + 1 MAIL
  FROM TXT) — all conditional on `mail.outbound_smtp`.
- **SES sandbox is a real, separate blocker** even with the flag on: verify
  individual recipient addresses for testing now; request production access
  (AWS Support Console, ~24h, manual, cannot be automated) before relying on
  this for real users.
- Deferred: per-tenant "from" domains (would reopen the tenant-mail-DNS
  question deliberately avoided here); the actual `mail` stack (inbound,
  mailboxes); DMARC record for the root domain (not requested, not added).
