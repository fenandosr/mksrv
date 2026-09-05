# ADR 0023: per-tenant Keycloak login branding

- Status: Accepted
- Date: 2026-09-04
- Milestone: M24

## Context

`tenants/<id>.yaml` already carried a `branding: {primary, logo_data_uri}`
block, but it only ever fed the Cloud-IT VPN desktop client's own UI (via
`configd`'s `tenant.json`). Keycloak — the actual login page every tenant
member sees — used the stock `keycloak.v2` theme for every realm, unbranded.

## Decision

- **Two colors, not more.** `branding` gains `secondary` alongside the
  existing `primary`. `background`/`text` overrides are deliberately not
  offered: `keycloak.v2` (PatternFly-based) ships accessible contrast out of
  the box, and mksrv has no way to validate that an operator-chosen
  background/text pair stays readable. `primary` (CTA buttons, links) +
  `secondary` (hover/focus accents) covers "this looks like our brand"
  without touching that surface. Additive later if a tenant genuinely needs
  it — the schema and CSS template are structured so it's a pure addition.
- **Logo stays a data URI, never a file.** It's embedded directly as a CSS
  `background-image: url("data:...")` in the rendered stylesheet — there is
  no separate asset file to get onto the host, no binary-through-template
  problem. The value is unchanged from the field the VPN client already
  reads, so one `logo_data_uri` now serves both surfaces.
- **CSS only, never the `.ftl` templates.** Keycloak's login HTML lives in
  FreeMarker templates that shift between major versions; a custom
  `theme.properties` (`parent=keycloak.v2`, `import=common/keycloak`) plus one
  `login.css` survives a Keycloak upgrade far better than forked HTML would.
  The selectors targeting `keycloak.v2`'s PatternFly classes are a first pass
  — verify against a live login page and adjust; that part of the CSS is the
  one piece of this milestone not proven against a running Keycloak in this
  session.
- **Theme placement**: each tenant gets `/opt/keycloak/themes/<id>` as a
  sibling of Keycloak's own bundled themes (`Volume=.../themes/<id>:/opt/
  keycloak/themes/<id>:Z,ro`, one per tenant in `render.Context.TenantIDs`) —
  not a replacement of the themes root, so `parent=keycloak.v2` still resolves.
- **The directory-must-exist-first hazard, again.** Podman does not create a
  missing bind-mount source (the same class of bug that broke
  `mksrv-node-exporter.service`, M23). Here the blast radius is worse: a
  missing tenant theme directory fails the *entire* Keycloak container, not
  just metrics for one host — every tenant's login breaks, not just the new
  one's. `deployHost` therefore calls `ensureTenantThemeDirs` (a plain
  `mkdir -p` over every declared tenant ID) immediately before deploying
  `identity`, on every `mksrv apply` — independent of whether `tenant apply`
  has ever run for that tenant. Adding a tenant to the workspace is safe
  before its branding is provisioned; Keycloak just falls back to its default
  theme for that (still-unthemed) realm.
- **Restart, always.** `provisionTenantBranding` (called from `tenant apply`,
  after every other Keycloak-touching step in that run) writes the theme
  files then unconditionally restarts `mksrv-keycloak.service` once — no
  content-diffing, matching `reconcilePostgREST`'s own unconditional-restart
  style. The operator confirmed a Keycloak restart per `tenant apply` run is
  acceptable.
- **`RealmSpec.LoginTheme`**: `EnsureRealm` now decodes the realm's current
  `loginTheme` from the same GET it already does for existence-checking, and
  issues a `PUT /realms/{realm}` only when it differs — idempotent, no
  realm-wide read-modify-write beyond that one field.

## Consequences

- No new secrets, no Terraform change.
- A new tenant added to the workspace is deployable via `mksrv apply` before
  `tenant apply` ever runs for it — Keycloak degrades to its default theme,
  not a crash.
- `mksrv tenant apply` now always restarts Keycloak once, even when only
  non-branding fields changed for a tenant (users, groups, clients) — a
  deliberate simplicity trade the operator accepted over diffing theme
  content.
- Deferred: `background`/`text` overrides (opt-in, additive, only if asked
  for); Grafana/pgAdmin branding (no realistic low-effort theming path for
  either today).
