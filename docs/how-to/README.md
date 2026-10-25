# How-to guides

Task-oriented. Each guide assumes you already have a working workspace (see the
[quickstart tutorial](../tutorials/quickstart.md)) and want to accomplish one
specific thing.

- [Migrate from mkserver scripts](migration.md) — the manual cutover sequence
  from the legacy scripts to mksrv-managed infrastructure.
- [Operator SMTP](mail-smtp.md) — enable Keycloak's own transactional email
  (forgot-password, verification) via SES, including the sandbox and
  troubleshooting.
- [Tenant mail hosting](tenant-mail.md) — give a tenant real inbound/outbound
  mailboxes on its own domain via the shared `mail` stack.
- [Tenant web endpoints](tenant-web.md) — terminate TLS at the edge for a
  tenant's own hostname and reverse-proxy to a mesh node, with an optional
  Keycloak SSO gate.
- [Tenant login branding](branding.md) — set a tenant's login-page colors and
  logo, convert a logo to a data URI, and iterate on the theme CSS.
- [Secrets (OpenBao)](secrets.md) — bring up the cluster, per-tenant policies
  and AppRoles, encrypting PII columns with Transit, and human OIDC login.
- [Backup and restore](backup.md) — what's in a daily snapshot, running one
  on demand, and restoring a tenant database, OpenBao, or a Keycloak realm.
- [Tenant local hosts](tenant-local-hosts.md) — run `meshnode`/`gitea`/
  `codeserver` on a tenant's own laptop or workstation, no SSH, no cloud
  provisioning.
