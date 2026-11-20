# ADR 0043: A tenant's DNS zone can be created and owned by mksrv

- Status: Accepted
- Date: 2026-11-20

## Context

A tenant with its own domain (`dns_override: {provider: route53, zone_id: …}`,
ADR 0010) needs a Route 53 hosted zone that **already exists**: mksrv only
writes records into it. The existing tenants' zones were created by hand. A new
tenant that brings a freshly bought domain had no way to have mksrv administer
that domain end to end; someone had to create the zone outside Terraform and paste
its id into the YAML, and the registrar delegation and the zone itself stayed
untracked.

## Decision

`dns_override.create_zone: true` (route53 only, mutually exclusive with
`zone_id`) makes Terraform create the hosted zone for the tenant's `base_domain`
(`aws_route53_zone.tenant`), and mksrv uses it exactly like a declared zone: the
`web:` A records, the SES verification TXT, the DKIM CNAMEs, the `mail:`
MX/SPF/DMARC computed from the block, and DKIM published by `tenant apply`.

- **Plan-time safety.** The id of a zone created in the same apply is unknown at
  plan time, and Terraform cannot key a `for_each` or a conditional on an unknown
  value. So every *decision* ("does this tenant have a zone?") is built from the
  configuration only (`local.tenant_has_zone`, `local.tenant_create_zone`), and
  only the zone id *value* (`local.tenant_zone_id`) may be unknown. A
  `terraform plan` from a workspace with a new `create_zone` tenant was run
  against the live state: it adds only that tenant's resources and **changes
  nothing of any existing tenant**.
- **`prevent_destroy`.** The zone holds the live DNS of a domain, so deleting a
  YAML line must never delete it. The consequence: `mksrv destroy` of the whole
  infra stops at this resource until it is removed deliberately.
- **Delegation stays outside mksrv.** The registrar must point the domain at the
  zone's name servers. They are an output (`tenant_zones`) and `mksrv apply`
  prints them. Until the registrar delegates, the domain does not resolve, but
  the records are already in place (SES verifies once DNS propagates, and the
  edge can issue a certificate only after).
- **Semantic validation** (`mksrv validate`): `create_zone` requires route53 and
  cannot be combined with `zone_id`; `web:`, `mail:` and `dns:` accept a created
  zone where they used to demand a `zone_id`.

## What a route53 tenant gets automatically (unchanged, ADR 0038)

An SES domain identity, its verification TXT, three DKIM CNAMEs and a dedicated
IAM user for SMTP, all in the tenant's own zone. SPF, DMARC and MX come from the
`mail:` block when the tenant declares one (`mail.hosted` for mailboxes).

## Consequences

- A new domain is managed from the first apply, with no hand-made zone.
- Existing tenants are untouched; `zone_id` keeps working as before.
- The state holds one more resource per such tenant; the zone's name servers are
  in `outputs.json`.
- Not covered: registering the domain, or changing its registrar's name servers
  (not an AWS API when the registrar is external).
