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
| `sso` | `false` (default). `true` gates the hostname behind a Keycloak session at the edge (ADR 0030) — see below. |
| `sso_groups` | optional list of realm groups (`admin`/`dev`/`apps`/`vpn`); only members of one of them pass the gate. Requires `sso: true`. |

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

## `sso: true` — a Keycloak gate at the edge

```yaml
web:
  - hostname: jupyter.mcps-epcm.org
    target: mcps-hpc-01.prod.mksrv:8000
    sso: true
    sso_groups: [dev, admin]      # optional
```

The hostname is public, but the edge refuses to proxy until the client has a
valid Keycloak session for the tenant realm. `mksrv tenant apply`:

- creates a confidential client `<id>-websso` in the realm (callbacks for every
  `sso` hostname, `groups` claim);
- runs one **oauth2-proxy** container on the edge per SSO tenant, on a loopback
  port, covering every `sso` hostname in the apex (shared `.<base_domain>`
  cookie);
- the Caddy fragment `forward_auth`s to it: an unauthenticated request is bounced
  to `/oauth2/start`, an authenticated one is proxied with `X-Auth-Request-User`
  / `-Email` / `-Groups`.

The origin can trust those headers for true SSO (JupyterHub
`RemoteUserAuthenticator`, Grafana `auth.proxy`, Gitea `REVERSE_PROXY`) or ignore
them and just benefit from being unreachable without a realm session.

Dropping the last `sso` entry tears the container down. `sso` + `cdn` is rejected.

## VPN-only web services

For a service **only VPN users** should reach, the options are:

- **`web: sso: true`** (above) — the hostname is public but gated on identity.
  This is the recommended path; it works for every user regardless of VPN client.
- **`forwards:`** — a Cloud-IT VPN forward opens `127.0.0.1:<port>` locally.
  Works, but a hostname-pinned app (JupyterHub, Gitea with a fixed `ROOT_URL`)
  breaks when served from `127.0.0.1`.
- **`dns:` A → the node's tailnet IP (`100.64.x.y`)** — only works for users
  running **real Tailscale** joined to the tenant's Headscale user, *not* the
  Cloud-IT VPN app, which is forward-only and cannot route to a `100.64/10`
  address or an advertised `mesh_routes` subnet.
