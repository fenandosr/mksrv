# ADR 0030: `web[].sso` — a Keycloak SSO gate at the edge

- Status: Accepted
- Date: 2026-09-10
- Milestone: M30
- Refines: ADR 0028 (`web:` block), ADR 0016 (tenant RBAC groups)

## Context

ADR 0028's `web:` block puts a tenant hostname behind the edge: TLS at the edge,
reverse-proxy to the origin over the mesh. Access control is left entirely to the
origin app.

That is fine for an app with its own login (Gitea, a portal). It is not fine for:

- an app whose "auth" is a shared password or a network assumption (an internal
  dashboard, a Jupyter behind PAM that trusts the network);
- a tenant that wants **one** sign-on across several services, gated by realm
  group, without wiring OIDC into each app;
- the common ask when migrating a cluster off a bespoke VPN: "these services
  were VPN-only; keep them locked down, but at their real hostname."

The Cloud-IT VPN client is forward-only (loopback ports), so it cannot carry a
hostname-pinned web app. The alternative to a perimeter is an **identity** gate:
the edge refuses to proxy until the client has a valid Keycloak session.

Everything needed exists: the edge is public and runs Caddy with fragment
`import`; each tenant has a Keycloak realm with the RBAC groups and a `groups`
token mapper; `keycloak.EnsureClient` already creates confidential clients and
returns the secret; the edge already runs per-service containers (`configd`).

## Decision

### `web[].sso`

```yaml
web:
  - hostname: jupyter.mcps-epcm.org
    target: mcps-hpc-01.prod.mksrv:8000
    sso: true                       # default false
  - hostname: git.mcps-epcm.org
    target: mcps-hpc-01.prod.mksrv:3000
    sso: true
    sso_groups: [dev, admin]        # optional: restrict to these realm groups
```

`sso: true` requires `provider: edge`. `sso` + `cdn` is rejected for now (the
CloudFront origin-secret handshake and the oauth2-proxy cookie need to be
reconciled first).

### What `mksrv tenant apply` does

For each tenant with **any** `sso` web entry:

1. **Keycloak** — ensures a confidential client `<id>-websso` in realm `<id>`:
   `standardFlowEnabled`, PKCE S256, `redirectUris` = `https://<h>/oauth2/callback`
   for every sso hostname, `webOrigins` = those hostnames. The realm's `groups`
   mapper already applies.
2. **oauth2-proxy** — renders `/etc/containers/systemd/mksrv-websso-<id>.container`
   on the edge (`quay.io/oauth2-proxy/oauth2-proxy`, pinned), listening on
   `127.0.0.1:<4180 + sorted-id index>`:
   - `provider=keycloak-oidc`, `oidc-issuer-url=https://<keycloak>/realms/<id>`,
     `client-id=<id>-websso`;
   - `reverse-proxy=true`, `set-xauthrequest=true`, `pass-access-token=true`,
     `skip-provider-button=true`, `email-domains=*`;
   - `cookie-domain=.<base_domain>`, `whitelist-domain=.<base_domain>` — one
     proxy covers every sso hostname in the tenant apex, with a shared session
     cookie;
   - `upstreams=static://202` — auth-only, Caddy does the proxying;
   - client secret + a generated `cookie-secret` are `podman` secrets
     (`mksrv-websso-<id>-oidc`, `-cookie`), env-injected.
   - `sso_groups` is **not** set on the container — one proxy serves every sso
     hostname in the apex; group rules are enforced per hostname (below).
3. **Caddy fragment** — for each sso hostname
   (`25-web-<id>-<slug>.caddy`):
   ```
   git.mcps-epcm.org {
     handle /oauth2/* {
       reverse_proxy 127.0.0.1:4180
     }
     handle {
       forward_auth 127.0.0.1:4180 {
         uri /oauth2/auth?allowed_groups=dev,admin   # from sso_groups; omitted when unset
         copy_headers X-Auth-Request-User X-Auth-Request-Email X-Auth-Request-Groups
         @err status 401 403
         handle_response @err {
           redir * /oauth2/start?rd={scheme}://{host}{uri}
         }
       }
       reverse_proxy mcps-hpc-01.prod.mksrv:8000 {
         header_up Host {host}
         header_up X-Forwarded-Proto {scheme}
         flush_interval -1
       }
     }
   }
   ```
   `sso_groups` becomes oauth2-proxy's per-request `allowed_groups` query param,
   so one proxy enforces different group rules per vhost. A non-sso entry keeps
   the plain fragment from ADR 0028.
4. Reloads the edge Caddy; `daemon-reload` + restart the `websso` unit when its
   config changed.

Removing the last `sso` entry for a tenant tears down the container, its
secrets, and the unit.

### The origin

The origin sees `X-Auth-Request-User` / `-Email` / `-Groups` on every proxied
request. An app that can trust an upstream-auth header (JupyterHub
`RemoteUserAuthenticator`, Grafana `auth.proxy`, Gitea `REVERSE_PROXY`) gets true
SSO. An app that can't still benefits — it is unreachable without a realm
session, and it can read the headers for audit.

## Consequences

- **One more container per sso tenant on the edge.** ~30 MB RAM. Shares the
  edge's failure domain (already true for `web:` — ADR 0028).
- **The realm is the gate.** Revoke access by removing the user from the realm
  (or the `sso_groups`); no per-app user management.
- **Not zero-trust of the origin.** The edge→origin mesh hop is still trusted
  network; the gate is at the edge, not the app. An app that also does its own
  authz (Gitea) double-checks; one that trusts the header does not.
- **`cdn: true` + `sso` deferred.** Also deferred: per-path auth exemptions
  (a public `/health` on an otherwise-gated host); passing the access token
  through to the origin as a bearer for API calls; group-to-app-role mapping
  helpers.
- Touchpoints: `schemas/tenant.v1.json`, `internal/model.TenantWebEndpoint`
  (`SSO`, `SSOGroups`), `internal/cli/web.go` (client ensure, container render,
  secret push, SSO fragment, teardown), `internal/cli/tenant.go` (pass the
  Keycloak client into `provisionTenantWeb`), `docs/tenant-web.md`.
