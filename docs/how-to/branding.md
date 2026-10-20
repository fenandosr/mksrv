# Tenant branding

`tenants/<id>.yaml`'s `branding:` block drives both the Cloud-IT VPN desktop
client's UI and, since M24, the tenant's Keycloak login page:

```yaml
branding:
  primary: "#0C6D77"      # CTA button, focus rings, links
  secondary: "#16B8A6"    # the other end of the background gradient + accents
  logo_data_uri: "data:image/png;base64,..."
```

The login page is a glassmorphism design: the background is a gradient blended
from `primary` and `secondary` (over a dark scrim that keeps the form legible
whatever the two hues are), the card is a translucent `backdrop-filter` panel,
and the form text is light-on-glass. `secondary` defaults to `primary` when
unset — the gradient still renders, just single-hue.

See ADR 0023 for why it's deliberately just these two colors (not
background/text) and why the logo is a data URI, not a file.

## Converting a local logo to a data URI

```bash
# Linux
echo "data:image/png;base64,$(base64 -w0 logo.png)"

# macOS (no -w0 on base64)
echo "data:image/png;base64,$(base64 -i logo.png | tr -d '\n')"
```

Paste the full output as `logo_data_uri` in the tenant's YAML (one line, no
matter how long — YAML flow scalars don't need wrapping).

- Prefer SVG when you have the vector — smaller, scales perfectly at any
  size. `image/svg+xml` is accepted.
- For a raster logo, keep it small: aim under ~50 KB of actual file (the
  schema caps the data URI at ~1 MB, i.e. ~750 KB of source file, but a login
  page has no business shipping that much). Resize to roughly what a login
  header actually needs — a few hundred pixels wide is plenty.

## Applying it

```bash
mksrv tenant apply <id>
```

This renders the tenant's `theme.properties` + `login.css` onto the identity
host, sets `loginTheme` on the realm, and restarts Keycloak once. Every
tenant gets its own theme this way, even with no `branding` block at all —
`login.css.tmpl` falls back to the same default teal (`#0C6D77`) the VPN
client already uses when `primary` is unset, so an unbranded tenant gets a
consistent mksrv look rather than literal stock Keycloak. `tenant apply`
reasserts `loginTheme` every run, so it always wins over a manual change made
in the admin console.

## Iterating on the CSS

The shipped `stacks/identity/templates/login-theme/login.css.tmpl` targets
`keycloak.v2`'s PatternFly v5 class names, verified against a live login page.
The glass look is tunable through CSS custom properties near the top of the
template — `--mksrv-glass-blur`, `--mksrv-glass-tint`, `--mksrv-glass-radius`,
`--mksrv-scrim` — change those before reaching for new selectors. To iterate:

1. Open a real login page — trigger a flow from the VPN client or a tenant
   service, or `https://<keycloak-domain>/realms/<realm>/login-actions/reset-credentials`.
2. Open devtools, inspect the card, the header/logo area, and the primary button.
3. Edit `login.css.tmpl` and re-run `mksrv tenant apply <id>` (renders the
   theme, sets `loginTheme`, restarts Keycloak once).

`color-mix()` and `backdrop-filter` are wrapped in `@supports` with opaque
fallbacks, so an old browser degrades to a solid card on a flat gradient.

## Restore literal stock Keycloak (no mksrv theme at all)

`tenant apply` always assigns the tenant's own theme (`mksrv-<id>`) and
reasserts it every run. To go back to Keycloak's unmodified theme, unset
`loginTheme` by hand in the admin console (`Realm settings → Themes → Login
theme → (unset)`) and don't run `mksrv tenant apply` for that tenant again
afterward — `EnsureRealm` never deletes anything, but it will re-set
`loginTheme` back to `mksrv-<id>` the next time it runs.
