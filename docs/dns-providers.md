# DNS providers

The engine presents one desired-record model and provider-specific Terraform
implementations.

| Provider | Milestone | Behavior |
|---|---:|---|
| Route 53 | M1 | Create A/CNAME records in a zone selected by ID or name |
| Manual | M1 | Create no resources; return copy/paste-ready pending records |
| Cloudflare | M6 | Create records with optional proxying and secret token reference |
| RFC2136 | M6 | Dynamic updates to BIND/Knot-compatible authoritative DNS |

A tenant whose `base_domain` is an independent apex sets `dns_override:
{provider: route53, zone_id: <zone>}`; `module "dns_tenant"` then writes that
tenant's declared `dns` records into its own zone with `allow_overwrite = false`
(ADR 0011). Cloudflare and RFC2136 `dns_override` providers remain deferred.

Secrets such as Cloudflare tokens and RFC2136 keys are references only; values
must resolve from SOPS or SSM at runtime and must not enter Terraform state or
logs.
