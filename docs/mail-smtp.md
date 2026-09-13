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

1. `mksrv apply && mksrv tenant apply`. The tenant-apply step also flips
   `resetPasswordAllowed` / `loginWithEmailAllowed` on each realm, so the
   "Forgot password?" link appears on the login page (it's off in a fresh
   Keycloak realm, and pointless without SMTP anyway).
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
  (`internal/aws.DeriveSESSMTPPassword`) is verified against a live SES SMTP
  endpoint and pinned by a regression test, so an auth failure here is almost
  always a stale credential — re-run `mksrv apply && mksrv tenant apply` to
  re-mirror the current IAM key into SSM and back onto the realm.

## Relaying the `mail` stack's outbound mail through SES

Different problem, same SES credential: AWS throttles/blocks outbound TCP
port 25 on EC2 by default (an account-level anti-spam measure, lifted only
via an AWS Support request — mksrv/Terraform cannot do this for you, and it
isn't required for `outbound_smtp` above, which only ever uses port 587).
Confirmed live on a hosted-mail tenant: inbound delivery worked perfectly,
but every outbound delivery attempt to a real recipient MX timed out
silently — nothing in mksrv's own config was wrong, the network path out on
port 25 just never got a response.

```yaml
mail:
  outbound_smtp: true    # required — this is where the SES credential comes from
  relay_outbound: true
```

```bash
mksrv apply             # (if outbound_smtp wasn't already on) creates the SES identity + IAM user
mksrv tenant apply      # reconcileMailRelay: pushes the SES SMTP credential as a podman Secret
mksrv deploy --stack mail   # re-renders the mailserver unit with RELAY_HOST/RELAY_PORT and restarts it
```

Run in that order — the podman secrets have to exist before the container
starts referencing them. Once on, docker-mailserver's own relay-hosts
feature routes **every** domain it manages through SES on port 587 (no
per-domain config needed); nothing changes for inbound mail, which was never
affected by the port 25 restriction (that's the same port, but the *inbound*
direction — someone else's MTA connecting *to* mksrv on 25 — was reachable
the whole time).

Also a legitimate default, not just a workaround for the port 25 block: it
keeps the shared mailserver's own IP reputation out of the picture entirely.
SES absorbs bounce/complaint handling and has an established sending
reputation; a single compromised mailbox sending spam through direct MX
delivery would otherwise risk poisoning deliverability for every other
hosted-mail tenant sharing that IP.

Every domain relayed this way needs its own **verified SES identity** (DKIM
tokens as CNAME records in that domain's zone) — SES rejects sending as an
unverified FROM domain in production mode. `mail.hosted: true` does not
verify one for you today; a tenant either already has one (as `mcps-epcm.org`
did, predating its migration to mksrv) or needs one set up by hand
(`aws sesv2 create-email-identity`) before enabling the relay.

## Full mailboxes for a tenant (not this stack)

This page is `mail.outbound_smtp` — outbound-only, operator domain only, for
Keycloak's own transactional email. A tenant that wants real inbound + outbound
mailboxes on its own domain (`@acme.example.com`) is a different feature: the
`mail` stack + a tenant's `mail:` block — see `docs/tenant-mail.md` and
ADR 0032.
