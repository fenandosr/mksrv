# ADR 0028: Tenant public web endpoints on the edge (`web:` block)

- Status: Accepted
- Date: 2026-09-08
- Milestone: M28
- Supersedes: the "Model A out of scope" decision in ADR 0011 (M7)

## Context

ADR 0011 gave a tenant `forwards` / `dns` / `mesh_routes` and deliberately chose
**Model B** for web services: "the tenant's cluster runs its own ingress and
TLS; mksrv only creates the DNS record pointing at the cluster's public
address." Model A — the mksrv edge terminating TLS for a tenant hostname — was
ruled out to keep the operator's infrastructure off the tenant's data path.

In practice that leaves a tenant running a public web app (Nextcloud, a portal,
a status page) on a private or mesh-only node with no public front door. Their
options are all heavy: stand up a public VPS as a reverse proxy, or adopt
Cloudflare Tunnel — which needs the tenant's apex zone moved to Cloudflare and
puts a third party in the TLS path.

Every piece Model A needs already exists:

- the edge is public (an Elastic IP), on the Headscale mesh, and runs Caddy with
  `import /etc/caddy/caddy.d/*.caddy` — the same fragment mechanism `identity`,
  `*.rest.<root>`, `grafana.` and `pgadmin.` already use;
- mksrv holds each tenant's Route53 `zone_id` (`dns_override`) and writes records
  into that zone through `module "dns_tenant"`;
- the tenant's app node reaches the edge over the mesh it already joined with
  `mksrv tenant mesh`.

## Decision

### The `web:` block

`tenants/<id>.yaml` gains an optional list:

```yaml
web:
  - hostname: files.mcps-epcm.org   # inside the tenant's own apex
    target: mcps-nextcloud.prod.mksrv:80
  - hostname: portal.mcps-epcm.org
    target: 10.60.0.9:8080
    cdn: true
```

| field | |
|---|---|
| `hostname` | FQDN **under `base_domain`** — validated the same way `dns` record names are; a hostname that isn't in the tenant apex is rejected. |
| `target` | `host:port` — a MagicDNS name of a tenant mesh node, or a private IP the edge can route to over the mesh. Same shape and validation as `forwards[].target`. |
| `provider` | `edge` (default and only value). The field exists so a `cloudflare` provider is a pure addition later, not a schema change. |
| `cdn` | bool, **default `false`**. `true` puts CloudFront + AWS WAF in front. |

Requires `dns_override: { provider: route53, zone_id: … }` (same as `dns:`).
`web` and `dns` may not both claim the same name. Max 16 entries.

### `provider: edge` (the default)

`mksrv tenant apply <id>`:

1. Renders a Caddy fragment onto the edge —
   `/var/lib/mksrv/caddy.d/25-web-<id>-<slug>.caddy`:
   ```
   files.mcps-epcm.org {
     reverse_proxy mcps-nextcloud.prod.mksrv:80 {
       header_up Host {host}
       header_up X-Forwarded-Proto {scheme}
       flush_interval -1
     }
   }
   ```
   Caddy obtains the certificate over **HTTP-01** (the edge's `:80` is already
   public), so stock `caddy:2.8` is enough — no `caddy-dns/route53` build. The
   proxy block is tuned for interactive apps: the original `Host` survives a
   chained proxy at the origin, `X-Forwarded-Proto` tells the app the client link
   is HTTPS, and `flush_interval -1` streams SSE / chunked responses through.
   (M30 adds an `sso: true` variant — see ADR 0030.)
2. Adds a Headscale ACL rule `fleet@ → <id>@:<ports>` for the ports named in
   that tenant's `web` targets. The fleet has no path to tenant nodes today;
   this is the minimum opening for the edge to reach the origin.
3. Reloads the edge Caddy (the existing `reload-caddy` hook).

`mksrv apply --infra-only`:

4. `module "dns_tenant"` writes `files.mcps-epcm.org` as an **A record → the
   edge's Elastic IP** into the tenant's Route53 zone, `allow_overwrite = false`
   — a name that already exists (the tenant's old manual record) fails the apply;
   the operator deletes the stale record first.

The edge is now in the tenant's web data path for that hostname — a deliberate,
per-hostname, opt-in reversal of ADR 0011's Model B.

### `cdn: true`

Terraform additionally provisions, per `cdn` hostname:

- an **ACM certificate** in `us-east-1` (CloudFront's requirement), DNS-validated
  through the tenant zone;
- a **CloudFront distribution** whose origin is the edge. Cache policy forwards
  all headers / cookies / query string (a dynamic app like Nextcloud is not
  cacheable) — the value here is TLS offload, Shield Standard DDoS absorption,
  and the WAF, not the CDN;
- an **AWS WAF v2 web ACL** (`CLOUDFRONT` scope): the AWS managed rule groups
  (Common, Known Bad Inputs, SQLi) plus one rate-based rule;
- the Route53 A record flips from `→ edge EIP` to an **alias → the CloudFront
  distribution**.

Origin protection: the edge Caddy fragment for a `cdn` hostname checks a shared
secret header that CloudFront injects (stored in SSM, referenced as an origin
custom header), so the origin can't be reached by bypassing CloudFront. The
edge security group's `:443` is additionally narrowed to the AWS-managed
`com.amazonaws.global.cloudfront.origin-facing` prefix list for `cdn` traffic.

### Ordering

The A record (Terraform) must exist before Caddy attempts HTTP-01, so
`mksrv apply --infra-only` precedes `mksrv tenant apply`. Caddy retries with
backoff if the record has not propagated yet, so a combined `mksrv apply` (infra
then hosts then a manual `tenant apply`) also converges.

## Consequences

- **The edge takes tenant web traffic.** Capacity and a shared failure domain:
  `provider: edge` without `cdn` means edge-down = that tenant's site down. ADR
  0027 already made the edge a harder SPOF; this compounds it. `cdn: true` moves
  TLS + DDoS absorption to CloudFront (edge-down still breaks the origin, but the
  blast radius and attack surface shrink). The edge stays cattle —
  `mksrv apply` rebuilds it.
- **New ACL surface.** `fleet@ → <id>@:<ports>` per web entry. Scoped to the
  ports actually referenced; the tenant owns those nodes.
- **Mesh latency** on the edge → origin hop (WireGuard). Fine for file storage /
  a portal; a latency-critical app would feel it.
- **Cost.** `provider: edge` is `$0`. `cdn: true` ≈ AWS WAF `$5`/mo +
  `~$1`/managed-rule-group + CloudFront `$0.085`/GB egress + request charges — a
  few dollars a month for a small tenant service.
- Touchpoints: `schemas/tenant.v1.json`, `internal/model.Tenant`,
  `internal/workspace/semantic.go` (hostname ∈ `base_domain`, target shape, no
  `web`/`dns` name clash, `route53` `dns_override` required), a new
  `internal/cli/web.go` (fragment render + ACL rule), `internal/headscale.Policy`
  (the `fleet@ → <id>@` rule takes web ports), `infra/root` +
  `modules/dns` (the web A records + ACM validation records) + a new
  `modules/tenant-cdn` (CloudFront + WAF, `count` on `cdn`), `docs/tenant-web.md`.
- Deferred: `provider: cloudflare` (mksrv creates the tunnel + DNS via the CF
  API and emits the `cloudflared` config for the tenant to run — needs the
  tenant's zone, or a delegated subzone, on Cloudflare; a pure addition on this
  schema); path-based routing and arbitrary per-route header rules; caching
  policy tuning for genuinely cacheable tenant sites; per-AZ edge / NAT for
  control-path HA (ADR 0027's deferred item).
