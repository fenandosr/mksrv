# Tenant web endpoints (`web:`)

A tenant that runs a web app on a private or mesh-only node — Nextcloud, a
portal, a JupyterHub — can have the mksrv edge terminate TLS for a hostname in
its **own apex** and reverse-proxy to that node over the mesh (ADR 0028). This
is *Model A*: opt-in per hostname, unlike `dns:` which only writes a record.

```yaml
# tenants/<id>.yaml
dns_override:
  provider: route53
  zone_id: Z0…            # the tenant's own hosted zone
web:
  - hostname: files.mcps-epcm.org
    target: mcps-nextcloud.prod.mksrv:80
```

| field | |
|---|---|
| `hostname` | an FQDN **inside `base_domain`**. |
| `target` | `host:port` — a MagicDNS name of a tenant mesh node (`mcps-nextcloud.prod.mksrv:80`) or a private IP the edge routes to over the mesh. |
| `provider` | `edge` (default and only value today). |
| `cdn` | `false` (default). `true` is reserved for CloudFront + WAF and is not implemented yet — it fails validation. |

## What `mksrv` does

`mksrv tenant apply <id>`:

1. writes an edge Caddy vhost fragment
   (`/var/lib/mksrv/caddy.d/25-web-<id>-<slug>.caddy`) — the edge gets the
   certificate over HTTP-01 (its `:80` is public);
2. opens the Headscale ACL rule `fleet@ → <id>@:<origin ports>` so the edge can
   reach the tenant's node on the ports its `web:` targets name;
3. reloads Caddy. A `web:` entry that was removed has its fragment deleted.

`mksrv apply --infra-only`:

4. writes an **A record `hostname → the edge's Elastic IP`** into the tenant's
   Route53 zone (`allow_overwrite = false`). If the tenant already has a manual
   record for that name, delete it first or the apply fails.

Run `apply --infra-only` before `tenant apply` so the record exists when Caddy
attempts HTTP-01 (it retries with backoff either way).

## Prerequisites

- The tenant's app node is on the mesh (`mksrv tenant mesh <id> --hostname …`)
  and named in `target` by its MagicDNS name, **or** `target` is a private IP
  the edge can reach over an advertised `mesh_routes` CIDR.
- The tenant zone is on Route53 (`dns_override`).

## Trade-offs

- The edge is now in that hostname's data path — edge down = the site down.
  ADR 0027 already made the edge a harder single point of failure; this
  compounds it for `web:` hostnames.
- Edge → origin adds one mesh (WireGuard) hop. Fine for file storage or a
  portal; a latency-critical app would feel it.

## VPN-only web services

This block is for **public** hostnames. For a service only VPN users should
reach, don't use `web:` — point the A record straight at the node's tailnet IP
(`100.64.x.y`) and run TLS on the node (Caddy `acme_dns route53`, or plain HTTP
since the mesh is already WireGuard-encrypted). It resolves for everyone but
only routes with the mesh up.
