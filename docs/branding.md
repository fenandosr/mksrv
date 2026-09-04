# Tenant branding

`tenants/<id>.yaml`'s `branding:` block drives both the Cloud-IT VPN desktop
client's UI and, since M24, the tenant's Keycloak login page:

```yaml
branding:
  primary: "#0C6D77"      # CTA buttons, links
  secondary: "#F2A900"    # optional — hover/focus accents
  logo_data_uri: "data:image/png;base64,..."
```

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
`keycloak.v2`'s PatternFly class names as a first pass — confirm them against
your actual login page and adjust if they don't match:

1. Open `https://<keycloak-domain>/realms/<tenant-realm>/account/` (or
   trigger a login flow from the VPN client / a tenant service) in a browser.
2. Open devtools, inspect the header/logo area and the primary button.
3. Compare the classes you see to the ones in `login.css.tmpl`; edit the
   template and re-run `mksrv tenant apply <id>` to iterate.

## Restore literal stock Keycloak (no mksrv theme at all)

`tenant apply` always assigns the tenant's own theme (`mksrv-<id>`) and
reasserts it every run. To go back to Keycloak's unmodified theme, unset
`loginTheme` by hand in the admin console (`Realm settings → Themes → Login
theme → (unset)`) and don't run `mksrv tenant apply` for that tenant again
afterward — `EnsureRealm` never deletes anything, but it will re-set
`loginTheme` back to `mksrv-<id>` the next time it runs.
