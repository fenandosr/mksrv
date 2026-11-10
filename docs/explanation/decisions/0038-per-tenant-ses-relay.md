# ADR 0038: per-tenant SES sending identity, credential, and relay

- Status: Accepted (Phases A–C live; Phase D — extending to future hosted-mail
  tenants, retiring the shared credential where no longer needed — deferred)
- Date: 2026-11-09

## Context

Every tenant's Keycloak transactional email (password reset, email
verification) and every hosted-mail tenant's outbound relay (`mail stack`,
ADR 0032, port 25 blocked on EC2) used **one shared IAM user**
(`mksrv-<env>-ses-smtp`, `aws_iam_user.ses_smtp`) and **one SES domain
identity** (the operator's own root domain). Two consequences, found
onboarding real tenants:

1. **No isolation between tenants.** `ses:SendRawEmail` with no
   `Resource`/`Condition` scoping means a leaked shared credential — or any
   tenant-facing service handed it — could send mail as *any* verified
   identity in the account, not just that tenant's own domain.
2. **Three tenants' own domains already had their own verified SES domain
   identity and a dedicated legacy IAM user, created by hand, entirely
   outside Terraform.**
   Found live via `aws sesv2 list-email-identities` / `aws iam list-users` —
   neither showed up in `terraform state list`. A real, untracked dependency:
   if anyone cleaned up "orphaned" SES identities in the console, three
   tenants' mail would break with no indication why in this codebase.

## Decision

### Phase A — import what already exists

The three pre-existing identities (`aws_ses_domain_identity.tenant`,
`aws_ses_domain_dkim.tenant`, and `mcps`'s `aws_ses_domain_mail_from.tenant`)
brought under Terraform via `terraform import`, zero drift, zero behavior
change. Verified live: the imported DKIM tokens matched exactly what was
already published in each tenant's zone.

### Phase B — an identity + credential for every route53 tenant

`infra/root/main.tf`: `local.tenant_ses_domains` — every tenant with
`dns_override.provider == "route53"` (the same gate `web:`/`dns:` already
use; independent of `mail.outbound_smtp`/`mail.hosted`) gets:

- `aws_ses_domain_identity.tenant[id]` on `base_domain`, `aws_ses_domain_dkim.tenant[id]`.
- The `_amazonses.<base_domain>` verification TXT and the 3 DKIM CNAMEs,
  written into the tenant's **own** zone (`module.dns_tenant`'s records list,
  and a dedicated `aws_route53_record.tenant_ses_dkim` for_each — DKIM CNAME
  names embed the token, so, like the operator's own `ses_dkim`, they can't
  go through the generic records-list for_each keyed by name).
- `aws_iam_user.tenant_ses_smtp[id]` + a policy scoped with
  `Condition: StringLike: ses:FromAddress: *@<domain>` — the actual isolation
  property: a leaked tenant credential can only send as that tenant's own
  domain.
- Output `tenant_mail_smtp` (map by tenant id), mirroring the operator's own
  `mail_smtp` output shape.

A 4th tenant (`gtex`) had no prior identity at all — Phase B gave it a real
one for the first time, same mechanism.

### Phase C — mksrv actually uses it

- `internal/cli/mail.go`: `tenantSMTPSpecFor(ctx, id, tenant)` reads
  `outputs.TenantMailSMTP[id]`; a tenant's realm now gets its *own* sender
  (`FromDisplay` = tenant display name) instead of the operator's. Falls back
  to the shared `tenantSMTPSpec` when the tenant has no entry (no route53
  zone to verify a domain in) — no regression for a tenant that can't have
  its own identity.
- `internal/cli/mail_hosting.go`: `reconcileMailTenantRelay` writes
  docker-mailserver's native sender-dependent relay config —
  `postfix-relaymap.cf` (`@domain\t[email-smtp.<region>.amazonaws.com]:587`)
  and `postfix-sasl-password.cf` (`@domain\t<access_key_id>:<password>`), one
  line per domain of every `mail.hosted: true` tenant with a Phase B
  identity. Confirmed against the actual docker-mailserver docs (not
  memory): these two files are what `relay.sh` turns into
  `smtp_sender_dependent_authentication = yes` +
  `sender_dependent_relayhost_maps` + `smtp_sasl_password_maps`. The existing
  shared `RELAY_HOST`/`RELAY_USER`/`RELAY_PASSWORD` env vars stay as the
  fallback for any sender domain not in these files.

Validated live end-to-end (2026-11-09): a real message from a mailbox on
`mcps`'s own domain relayed through `email-smtp.us-east-1.amazonaws.com`
using `mcps`'s own credential, SES returned `250 Ok`, delivered. First time
this path — old or new — had ever actually been exercised; nothing was
broken by replacing it, because nothing had used it before.

### Incident: a tenant credential was rotated mid-rollout

A verification command's delimiter assumption was wrong (`cut -d' '` against
a tab-delimited file) and echoed `mcps`'s SASL password into the session
transcript. Remediated: `terraform apply -replace -target` recreated just
that one `aws_iam_access_key.tenant_ses_smtp["mcps"]` resource; the old key
was destroyed (confirmed via `aws iam list-access-keys`, only the new one
remains); SSM, Keycloak, and the relay files were re-synced from the new
value before the mailserver was restarted again.

## Consequences

- A leaked tenant SMTP credential is now scoped to that tenant's own domain
  only, not every verified identity in the account.
- Three previously-untracked AWS resources (per tenant) are now in Terraform
  state, visible to `plan`/`destroy` like everything else.
- SES sending quota/rate limits remain account+region-wide regardless — this
  does not give per-tenant throughput isolation, only identity/credential
  isolation and independent revocation.
- `gtex` gained a real SES identity for the first time; its DNS verification
  landed as part of the same change.
- Legacy IAM users found live (`mcps-ses-smtp-user`, `ses-smtp-huellagenica`)
  were left untouched — unclear what, if anything, still depends on them;
  deleting them was out of scope here and needs its own check first.
- Deferred (Phase D): extending the per-tenant relay config to `hg`/`bitabit`
  once (if) they actually host mailboxes (`mail.hosted: true`) rather than
  just documenting SPF/DMARC intent; deciding whether to retire the shared
  operator credential/identity once every tenant that needs one has its own;
  the `forwards:`-can-reach-a-DB-or-Vault-port gap noted for `tag:<id>-vpn`
  in ADR 0037 is a related, separate isolation question, not solved here.
