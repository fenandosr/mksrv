# Operator SMTP (Keycloak transactional email)

Enables Keycloak's "forgot password" / email verification flows. Not the
`mail` stack (still unimplemented — inbound, per-tenant mailboxes). See
ADR 0025 for the full rationale.

## Enable it

```yaml
mail:
  inbound: false
  outbound_smtp: true
```

```bash
mksrv apply           # creates the SES identity, DKIM, MAIL FROM domain, IAM user
mksrv tenant apply    # sets every realm's SMTP settings from it
```

## What gets created

- An SES sending identity for the operator root domain, sender
  `noreply@<root_domain>`.
- DKIM (3 CNAME records) and a custom MAIL FROM domain
  (`mail.<root_domain>`, 1 MX + 1 TXT/SPF record) — all in the **operator**
  Route53 zone. No tenant domain or DNS is touched.
- An IAM user scoped to `ses:SendRawEmail` only; its derived SMTP credential
  is mirrored into SSM (`/mksrv/{env}/mail/ses_smtp_user`,
  `.../ses_smtp_password`).

## The SES sandbox — read this before testing

A new SES identity starts in the **sandbox**: you can only send to
individually verified addresses, capped at 200/day. Turning
`outbound_smtp` on does not by itself make mail reach real users.

- **To test now**: AWS Console → SES → Verified identities → Create identity
  → verify one email address (e.g. the admin you're testing with). Takes a
  couple of minutes, no support ticket.
- **For real users**: AWS Console → Service Quotas (or SES → Account
  dashboard) → request production access. A manual AWS Support case,
  usually resolved within ~24h. mksrv/Terraform cannot do this for you.

## Verifying it end to end

1. `mksrv apply && mksrv tenant apply`.
2. Confirm in the SES console that the root domain identity is "Verified"
   (DKIM propagation can take a few minutes after the CNAME records land).
3. Verify one test recipient address in the SES sandbox (above).
4. On that tenant's realm login page → "Forgot password?" → enter the
   verified test address → confirm the email arrives.

## Troubleshooting

- **Domain stuck "Pending verification"**: DKIM CNAMEs haven't propagated
  yet, or the wrong zone got the records — check `mksrv apply`'s DNS output
  matches the operator zone.
- **Email never arrives / bounces**: almost always the sandbox — verify the
  recipient address, or request production access.
- **Keycloak shows an SMTP error when testing** (Realm settings → Email →
  "Test connection"): the derived SMTP password
  (`internal/aws.DeriveSESSMTPPassword`) has not been verified against a
  live endpoint in this codebase yet — if authentication itself fails (not a
  sandbox/delivery issue), that derivation is the first thing to double
  check against AWS's reference implementation.
