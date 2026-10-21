# Tenant file services (`files`)

Gives a tenant its own Nextcloud (web UI, sync clients, WebDAV) and Samba
(native SMB share) instance — one of each, on the host carrying `files`.
Dedicated Postgres database for Nextcloud (ADR 0040); Samba stores its files
on a plain named volume, no database.

```yaml
# tenants/<id>.yaml
stacks: [database, files]
```

```bash
mksrv tenant apply <id>
```

## What you get

- **Nextcloud**: `https://<id>.files.<root_domain>` (Caddy on the edge
  reverse-proxies to the host carrying `files`). Admin user `admin`,
  password in SSM `/mksrv/{env}/files/tenant_<id>_admin_password`.
- **Samba**: a single share named `files`, one account (`tenant`), password in
  SSM `/mksrv/{env}/files/tenant_<id>_samba_password`. Reachable only on the
  tailnet or loopback — SMB has no reverse-proxy story, so it is never public.
  Connect to `\\<data-host-tailnet-ip>:<port>\files` (the port is
  per-tenant — see below); `mksrv status` or
  `aws ssm get-parameter --with-decryption --name
  /mksrv/{env}/files/tenant_<id>_samba_password` to get the password.

## Ports

Both apps take a consecutive host port per tenant, assigned by sorted tenant
ID (same scheme PostgREST already uses) — up to ten tenants each:

| | Base port | Range |
|---|---:|---|
| Nextcloud | 8030 | 8030–8039 |
| Samba | 4450 | 4450–4459 |

Opened in the Headscale ACL automatically; no manual step.

## Samba and Windows

Windows' built-in SMB client cannot target a non-default port, so a
per-tenant port above 445 means no native `\\host\share` access from
Windows Explorer. Options: a third-party SMB client that supports a custom
port, or mount from Linux/macOS/WSL (`mount.cifs ...,port=<port>`, or
`smb://host:port/files` on macOS), which do support it.

## Restoring from a backup

Not yet covered by `mksrv backup` (`docs/how-to/backup.md`) — only Postgres,
OpenBao, Keycloak, and the `storage:`-declared dedicated volumes are
snapshotted today. `nextcloud_<id>`'s database is a normal Postgres database
on the same cluster as every other tenant database, so a manual `pg_dump`
works the same way `docs/how-to/backup.md`'s restore section documents for
`db_<id>`; the Nextcloud/Samba data directories are plain named podman
volumes (`podman volume export`/`import`) until this gets proper `storage:`
coverage.
