# Changelog

## Unreleased

- Add (M32 follow-up, ADR 0032): migrating a mailbox from another mail
  server no longer forces a password reset. docker-mailserver's
  `postfix-accounts.cf` hash format (`{SHA512-CRYPT}$6$...`) is portable —
  no per-installation secret involved — so an operator can seed a
  mailbox's existing hash directly into SSM
  (`/mksrv/{env}/mail/tenant_<id>_<local>_password_hash`, verbatim from the
  old server's own `postfix-accounts.cf`) before `mksrv tenant apply`;
  `reconcileMailboxes` checks for it first and uses it as-is, only
  generating a fresh password when it's absent (a genuinely new mailbox).
  `internal/secrets` gains `IsNotFound`, an exported check for "the SSM
  parameter doesn't exist" distinct from a real read failure, so a caller
  can treat "no pre-seeded hash" as an expected outcome instead of an
  error.

- Fix (infra): `mksrv deploy --stack mail` hard-failed on every first deploy
  to a host with no mailboxes provisioned yet — its `tcp`/993 health check
  retried for its full ~5 minutes and then failed, because
  docker-mailserver refuses to start Dovecot (so nothing ever binds :993)
  until at least one mailbox exists in `postfix-accounts.cf`, which
  `mksrv tenant apply` writes *after* this deploy. Switched to `type:
  command` (the existing no-op health-check escape hatch) — the
  container's own Quadlet `HealthCmd` still tracks real health
  continuously (`podman ps` / `systemctl status`). New regression test
  guards against a `tcp`/993 check on this stack specifically.

- Fix (infra): the mail server's Quadlet unit bind-mounts five host
  directories (its config/TLS bind mounts, plus the `maildata` volume's
  three subdirectories); Podman doesn't create a missing bind-mount source
  itself, so on a fresh host (or a volume newly bootstrapped) the container
  failed to start: `statfs /var/lib/mksrv/stacks/mail/config: no such file
  or directory`. Nothing in the stack wrote into those directories via
  `templates:` (they're populated later, by `mksrv tenant apply`'s direct
  SSH writes, or by docker-mailserver itself) — `stacks/mail/stack.yaml`
  now carries five trivial `.keep` placeholders whose only job is to make
  `DeployStack`'s write-time `mkdir -p` create the directory ahead of the
  container's first start. New regression test
  (`TestStackRendersMail`) also guards the `Hostname=`/`HostName=` Quadlet
  key fix from the previous PR.

- Fix (infra): `mksrv deploy --stack mail` wrote the mail server's Quadlet
  unit but `mksrv-mailserver.service` never existed for systemd to start —
  `stacks/mail/templates/mailserver.container.tmpl` used `Hostname=`
  (lowercase `n`); the real Quadlet key is `HostName=`. Podman's Quadlet
  generator drops a `[Container]` key it doesn't recognize and logs to the
  journal instead of failing the unit file outright — `mksrv deploy` itself
  reported success (the file was written), and the failure only surfaced
  one step later, restarting a unit that was never generated:
  `Unit mksrv-mailserver.service not found`.

- Fix (infra): `mksrv tenant apply` tried to generate DKIM for tenants that
  only had a documentation-only `mail:` block (`hosted` unset/false) —
  `provisionMail`'s DKIM loop gated on `t.Mail != nil` alone, not the
  `hosted` flag added two PRs ago, so it ran against a mail server those
  tenants never opted into and errored (`no container with name or ID
  "mksrv-mailserver" found`, since only the actually-hosted tenant's realm
  of concerns applies there). Both this loop and `mailTenants()` (the
  shared mailbox file) now share one `tenantMailHosted()` predicate, so
  they can't drift apart like this again.

- Fix (infra): the mail stack's computed SPF/DMARC values were manually
  wrapped in quotes (`"\"v=spf1 ... ~all\""`) — `aws_route53_record` adds
  the enclosing quotes to a TXT value itself, so this sent Route53 a
  doubly-quoted string and failed apply with `InvalidCharacterString
  (Value should be enclosed in quotation marks)`. The exact bug class
  `ses_dns_records` (a few lines down in the same file) already hit and
  documented a fix for in M25 — reintroduced here by not having read that
  comment before adding the mail stack's own SPF/DMARC computation.
  Removed the manual quoting; values are plain text now, matching
  `ses_dns_records`'s established convention.

- Add (M32 follow-up, ADR 0032): `mail.spf_includes` / `mail.dmarc_policy` /
  `mail.dmarc_strict` — let the computed SPF/DMARC match a domain's real
  requirements instead of mksrv's plain defaults (`v=spf1 mx ~all`,
  `p=quarantine`). Needed live: a tenant already had a working SES sending
  identity for its domain (`v=spf1 include:amazonses.com mx ~all`) and a
  stricter, aligned DMARC (`p=reject; adkim=s; aspf=s`) predating the mail
  stack migration — mksrv's defaults would have silently dropped the SES
  authorization and loosened the policy. `spf_includes` adds extra
  `include:` mechanisms ahead of `mx`; `dmarc_policy` (`none` \|
  `quarantine` default \| `reject`) and `dmarc_strict` (adds `adkim=s;
  aspf=s`) shape the DMARC record. A domain with pre-existing SPF/DMARC
  still needs one `terraform import` per record before the first
  `hosted: true` apply — Terraform won't overwrite a record it doesn't know
  about, it fails the create instead (documented in `docs/tenant-mail.md`).

- Add (M32 follow-up, ADR 0032): `mail.hosted` — the real opt-in switch for
  the shared mail stack. Without it, `mail.domains`/`dmarc_rua` alone is just
  documentation (e.g. a tenant recording SPF/DMARC intent ahead of an actual
  migration): `mksrv apply --infra-only` writes no DNS and
  `mksrv tenant apply` provisions no mailboxes/DKIM for that tenant.
  Surfaced live: two tenants already carried a `mail:` block from earlier,
  unrelated work, and once M32 merged, `mksrv apply --infra-only` started
  computing SPF/DMARC for both — outside what had actually been authorized.
  `mail` is a shared, non-`per_tenant` stack (the schema rejects listing
  `mail` in a tenant's own `stacks:` — "not tenant-consumable"), so unlike
  `database` this can't gate on stack membership; `hosted` is the dedicated
  flag instead.

- Fix (infra): `terraform plan`/`apply` failed with "all map elements must
  have the same type" for `var.tenants` as soon as one tenant's `web:` or
  `forwards:` used a shape another tenant (or another entry in its own list)
  didn't — e.g. the first tenant to declare `web:` at all, or a `forwards:`
  list mixing an `ssh` entry (with `ssh_alias`/`open`) and a `tcp` entry
  (without). `var.tenants` is now an explicit `map(object({...optional(...)}))`
  instead of `map(any)`, matching `internal/model.Tenant`'s JSON shape, so
  each tenant's optional blocks can genuinely vary. Fallout from that: an
  unset optional attribute is now a real `null`, not a missing-key error, so
  `try(t.web, [])`-style expressions in `infra/root/main.tf` that relied on
  the old "missing key errors, try() catches it" behavior needed
  `coalesce()` too (`try()` alone doesn't fall through on a legitimate
  `null`). `TenantForward` / `TenantWebEndpoint`'s optional fields dropped
  their `omitempty` tags so every list entry carries the same key set.

- Add (M32, ADR 0032): the `mail` stack is implemented — a shared
  `docker-mailserver` instance on the edge, real inbound + outbound mailboxes
  for tenants that opt in with a `mail:` block (`domains`, `inbound`,
  `dmarc_rua`, `mailboxes: [{address, name?}]`). `mksrv tenant apply`
  generates each mailbox's password, writes the shared `postfix-accounts.cf`
  (restarting only on change, aggregated across every mail tenant), copies in
  a TLS cert Caddy already holds for the shared hostname, and generates +
  publishes each domain's DKIM key — the one DNS write mksrv makes directly via
  the AWS SDK (`internal/aws.UpsertTXT`) instead of Terraform, since the key
  only exists after the server creates it. `mksrv apply --infra-only` writes
  MX/SPF/DMARC, fully computed from the tenant's own block. New SG ports
  (`25`/`465`/`587`/`993`, edge-only) and a dedicated `maildata` storage
  volume. **Writes to a tenant's own DNS zone** (MX/SPF/DMARC/DKIM) — scoped to
  tenants that explicitly opt in with `mail:`, same boundary as `dns:`/`web:`.

- Add (M31, ADR 0015 update): `mksrv tenant secret-id <id>` mints a named,
  response-wrapped OpenBao AppRole SecretID for a tenant's service — the operator
  stops copying the write-once bootstrap SecretID out of SSM by hand with the
  root token. `--name` (audit metadata), `--cidr` (bind to source networks),
  `--ttl` / `--num-uses`, `--list`, `--revoke <accessor>`. Runs under a
  least-privilege `mksrv-operator` AppRole (SecretID ops on `tenant-*` roles
  only), created once with root and self-contained after.

- Add (M30, ADR 0030): `web[].sso: true` (+ optional `sso_groups`) gates a
  tenant web hostname behind a Keycloak session at the edge. `mksrv tenant
  apply` creates a confidential `<id>-websso` realm client and runs one
  oauth2-proxy container on the edge per SSO tenant; the Caddy fragment
  `forward_auth`s to it and passes `X-Auth-Request-User` / `-Email` / `-Groups`
  to the origin. Dropping the last `sso` entry tears the gate down. `sso` + `cdn`
  is rejected (`tenant.web.sso_cdn`).

- Change (M28/M30, ADR 0028): every `web:` Caddy fragment now proxies with
  `header_up Host {host}` (survives a chained proxy at the origin),
  `header_up X-Forwarded-Proto {scheme}` (OIDC redirects, OnlyOffice), and
  `flush_interval -1` (SSE / long-poll / chunked UIs stream instead of buffering;
  WebSockets already did).

- Change (M7, ADR 0011): `mksrv tenant mesh <id>` prints a `tailscale up` that
  joins the tenant node as a leaf — `--accept-routes=false --accept-dns=false`
  (never override a box with its own LAN/resolver) — and appends
  `--advertise-routes=<cidrs>` from the tenant's `mesh_routes`, with a reminder
  to enable `net.ipv4.ip_forward` and run `headscale nodes approve-routes` on
  the edge.

- Add (M29, ADR 0015 update): the `tenant-<id>-dev` OpenBao policy (and the
  tenant AppRole) gains `transit/hmac/<id>` and `transit/datakey/plaintext/<id>`.
  `hmac` backs blind-index columns for equality search over encrypted data
  (pin `key_version=1`; per-tenant key, so no cross-tenant correlation);
  `datakey` backs envelope encryption for bulk PII (one wrapped key per row,
  batch-unwrapped on read). `rewrap` / `rotate` stay `admin`-only. Apply with
  `mksrv tenant apply`. `docs/secrets.md` documents both patterns.

- Security (M29, ADR 0029): `mksrv_anon` — the role PostgREST uses for
  token-less requests — **no longer gets a blanket `SELECT`** on the tenant's
  application schema. The PostgREST URL is public, so anonymous read access is
  now opt-in per table (`GRANT SELECT ON <schema>.<table> TO mksrv_anon`);
  `mksrv_app` (authenticated, group-gated) keeps the schema default.
  `mksrv tenant apply` revokes the old blanket grant to heal databases created
  before this change.

- Add (M29, ADR 0029): optional `database:` block in `tenants/<id>.yaml` —
  `postgrest` (default `true`; `false` tears down the PostgREST container, its
  edge Caddy vhost, and the `*.rest` A record / cert SAN, leaving Postgres
  reachable only over the VPN), `schema` (default `"app"`; drives
  `PGRST_DB_SCHEMAS` / `PGRST_DB_PRE_REQUEST`), `extensions` (allow-listed
  contrib extensions `CREATE EXTENSION`'d in `db_<id>`), and `connection_limit`
  (caps `<id>_login` against the shared Patroni `max_connections`). New
  validation codes `tenant.database.no_stack` / `.schema` / `.extension` and the
  warning `tenant.database.schema_unused`.

- Feature (M28, ADR 0028): a `web:` block in `tenants/<id>.yaml` —
  `[{hostname, target, provider?, cdn?}]`. With `provider: edge` (the default)
  `mksrv tenant apply` renders an edge Caddy vhost fragment (HTTP-01 cert on
  stock `caddy:2.8`), opens `fleet@ → <id>@:<origin ports>` in the Headscale
  ACL, and `mksrv apply --infra-only` writes an A record `hostname → edge EIP`
  into the tenant's Route53 zone (`allow_overwrite = false`). The edge now
  terminates TLS and reverse-proxies to a tenant node over the mesh — Model A,
  opt-in per hostname, superseding the "Model A out of scope" line in ADR 0011.
  `hostname` must be within `base_domain`, must not clash with a `dns:` record,
  and needs a `route53` `dns_override`. `cdn: true` (CloudFront + WAF) is
  reserved and fails validation for now. Removing a `web:` entry deletes its
  fragment on the next `tenant apply`.

- Change (M27, ADR 0027): only the `base` host (edge) is public now. On a
  multi-host fleet every other host moves to a private subnet with **no
  Elastic IP** (~$3.65/mo each — the distributed profile drops ~$14.6/mo);
  edge keeps its one EIP and runs as the NAT for the private subnets
  (firewalld masquerade, `source_dest_check = false`, `net.ipv4.ip_forward`)
  and the SSH bastion. `internal/ssh.Target` gains `Jump`; `Dial` /
  `FetchHostKey` tunnel through it, and `openFleet` wires every private AWS
  host's jump to edge. `mksrv bootstrap` / `apply` / `host trust` order the
  base host first so the bastion exists before the CLI reaches the hosts
  behind it. A free S3 gateway VPC endpoint keeps `backup` off the NAT.
  `BootstrapVersion` 10 → 11. Single-node fleets and `existing` hosts are
  unchanged. **Not an in-place migration** — destroy + `mksrv apply` (edge
  keeps its EIP, so operator-zone DNS is untouched).

- Change (M26, ADR 0026): the per-tenant Postgres RBAC roles (`<id>`,
  `<id>_app`, `<id>_web`, `<id>_anon` — five per tenant with `<id>_auth`) become
  four **cluster-global** buckets `mksrv_owner` / `mksrv_app` / `mksrv_anon` /
  `mksrv_web`. Only the two roles that carry a password and the
  `GRANT CONNECT ON db_<id>` gate stay per-tenant: `<id>_login` (humans over the
  VPN) and `<id>_auth` (PostgREST authenticator). PostgreSQL object privileges
  are per-database, so a global bucket used in a `db_<id>` session only ever
  sees that tenant's data — isolation is unchanged. `<id>_login` sessions start
  as `mksrv_owner` (`ALTER ROLE … SET role`) so DDL ownership and default
  privileges are uniform; `session_user` and the logs still name the tenant.
  The `role` claim is the constant `mksrv_web`; `app.pgrst_pre_request()` is one
  definition. `pg_roles` no longer lists four roles per tenant.
  **Rollout (no data yet):** drop every `db_<id>` and the old `<id>` /
  `<id>_{app,web,anon}` roles on the Patroni primary, then `mksrv tenant apply`
  (recreates everything + rewrites the Keycloak mapper) and redeploy the
  PostgREST containers for `PGRST_DB_ANON_ROLE=mksrv_anon`.

- Fix (M24): the per-tenant login theme never actually loaded. The realm's
  `loginTheme` was set to `mksrv-<id>` (`cli.themeName`), but the theme
  directory — the `mkdir` in `ensureTenantThemeDirs`, the write target in
  `provisionTenantBranding`, the `stacks/identity` per-tenant template dst, and
  the bind mount in `keycloak.container.tmpl` — all used the bare `<id>`. So
  Keycloak looked for a theme that wasn't on disk and silently served the
  default `keycloak.v2` for every realm. All four now use `mksrv-<id>`.
  Remediation on a live fleet: `mksrv apply` (re-renders the Keycloak unit with
  the corrected mounts + `mkdir`s the new dirs) then `mksrv tenant apply` (writes
  the theme files there, restarts Keycloak); the stale `themes/<id>/` dirs on
  the identity host can then be deleted.

- Feature (M24): the tenant login theme is now a glassmorphism design — a
  background gradient blended from `branding.primary` and `branding.secondary`
  (with a dark scrim that keeps text legible whatever the two hues are), a
  translucent `backdrop-filter` card, and light-on-glass form controls. Still
  CSS-only (no FreeMarker overrides). `branding.secondary` now defaults to
  `branding.primary` when unset, so the gradient always renders (single-hue
  until a second colour is set) rather than the block being omitted. New
  tunables as CSS custom properties (`--mksrv-glass-blur` / `-tint` / `-radius`,
  `--mksrv-scrim`); `color-mix()` / `backdrop-filter` sit behind `@supports`
  with opaque fallbacks. Selectors verified against a live `keycloak.v2` login
  page. Re-run `mksrv tenant apply <id>` to pick it up.

- Fix: the `cache` stack bind-mounted `users.acl` as a **single file**. Every
  `mksrv tenant apply` rewrites that file by atomic rename (new inode), so the
  Redis container kept seeing the inode from container start — a seed with no
  tenant users — and the `ACL LOAD` that follows reloaded stale data. Result:
  a tenant's Redis password matched SSM and the OpenBao mirror but Redis
  answered `WRONGPASS`, and no tenant ACL user ever actually loaded on a fresh
  fleet. The container now bind-mounts the `acl/` directory (`aclfile` moved to
  `/etc/redis/acl/users.acl`); live `ACL LOAD` picks up rewrites without a
  restart. On an already-running fleet, `systemctl restart mksrv-redis` once
  after upgrading loads the current file; the old
  `/var/lib/mksrv/stacks/cache/users.acl` can be deleted.

- Fix: `EnsureRandom` generated secrets with `base64url`, whose alphabet
  includes `-`. A secret starting with `-` is a footgun for getopt-based tools
  (`redis-cli -a`, though redis-cli's own parser tolerates it), `PGPASSWORD=`,
  and `scheme://user:pw@host` URLs. Generated secrets are now `[A-Za-z0-9]`
  only, same length. Existing secrets are unchanged — rotate (delete the SSM
  parameter + re-run the relevant `mksrv … apply`) to pick up the new format.

- Fix (M20): Patroni's `pg_hba` allowed only the VPC CIDR and `127.0.0.1`, so
  a connection reaching `:5432` via `PublishPort` (the Cloud-IT VPN
  `database` forward, anything over the mesh) was rejected — podman SNATs
  published-port traffic to the bridge gateway (`10.89.x.1`), outside
  `10.20.0.0/16`. Added `host all all samenet scram-sha-256`. This is in
  `bootstrap.pg_hba`, so it only lands on a **fresh** cluster; a live cluster
  needs the line appended to `pg_hba.conf` on each node + `patronictl reload`
  once (see the PR / `mksrv postgres bootstrap` notes).

- Fix (M4): `mksrv tenant apply` also wrote the configd tenant roster to SSM
  (`/mksrv/<env>/identity/configd_tenants`), which blew the 4096-char
  Standard-tier value limit once the roster grew (5 built-in forwards × 3
  tenants). The roster is derived from `tenants/*.yaml`, regenerated every
  run, and never read back from SSM — it only needs to reach the edge as a
  podman secret, which it still does. The SSM write is gone.

- Refactor: `demoForwards` → `builtinForwards` (`demoTargets` →
  `builtinForwardTargets`, `fleetDemoTargets` → `fleetForwardTargets`). The
  "demo" name is from M4 when these were a tunnel smoke-test; they're now the
  real per-tenant service set. Also: `openbao` added to `reservedForwardIDs`
  and the built-in-forward count bumped 4→5 (both missed when the openbao
  forward landed) — a tenant `forwards:` entry with `id: openbao` now fails
  validation as reserved instead of silently duplicating the built-in.

- Feature (M12): a tenant carrying the `openbao` stack now gets an `openbao`
  forward in its Cloud-IT VPN config (the raft leader's `:8200` — standbys
  forward requests anyway). Without it there was no way to reach OpenBao
  from a tenant machine that isn't on the tailnet directly, so `bao login
  -method=oidc` from a laptop just failed to resolve the node.

- Fix (M12/M20): the tenant DB/cache secrets mksrv mirrors into OpenBao KV
  (`kv/tenants/<id>/database`, `.../cache`) hard-coded `host=mksrv-postgres` /
  `host=mksrv-redis` — container names that don't exist on the distributed
  profile. The DB secret now carries the Patroni node list over the mesh
  (with `target_session_attrs=read-write`), the cache secret the `cache`
  stack host's mesh name; standalone keeps the container names.

- Fix (M4): a Headscale API key is bound to a specific Headscale database, so
  an in-place relaunch (fresh Headscale) silently invalidated the one in SSM
  — `configd` then couldn't mint pre-auth keys and the VPN client got
  "502: could not mint a mesh key". `reconcileConfigd` reused the stored key
  without checking it; it now mints a fresh one every `mksrv tenant apply`
  and expires the rest.

- Fix (M13/M20): `configd`'s built-in ("demo") forwards — the services the
  Cloud-IT VPN app shows out of the box — all targeted `<env>-data.<env>.mksrv`,
  a host that only exists in the old 2-host topology. On the distributed
  profile PostgREST/Redis live on `appd` and Postgres on the `core*` Patroni
  cluster, so every forward pointed at a name that doesn't resolve — the app
  connected to the mesh but listed no working services. The targets are now
  resolved from the fleet's actual shape (`fleetDemoTargets`): the `base`
  host for edge-health, the Patroni primary for raw `:5432`, the `database`
  / `cache` stack hosts for PostgREST / Redis. An absent host drops its
  forward instead of emitting a dead one.

- Fix (M24): the login theme set `parent=keycloak` (the old theme, whose DOM
  the M24 CSS selectors don't match) and `styles=css/login.css` alone, which
  *replaces* the base theme's stylesheet list rather than adding to it — the
  branded page came out unstyled or fell back. Now `parent=keycloak.v2` and
  `styles=css/styles.css css/login.css` (Keycloak resolves `styles.css` from
  the parent, `login.css` from the tenant theme).
- Fix (M25): the SMTP reconcile now also sets `resetPasswordAllowed` and
  `loginWithEmailAllowed` on the realm, so the "Forgot password?" link
  actually appears — it's off in a fresh Keycloak realm.

- Fix (M20): `mksrv tenant apply`'s database provisioning trusted
  `.mksrv/postgres.json`'s recorded primary blindly — a snapshot from the
  last `mksrv postgres bootstrap`. Any failover since (a rolling restart from
  a later `mksrv apply` touching the `postgres` stack included, e.g. M23
  adding `postgres-exporter`) leaves it stale, and DDL against a
  now-demoted replica fails with "cannot execute ALTER ROLE in a read-only
  transaction". `provisionDatabases` now checks the live Patroni leader
  (`patronictl list`, answerable from any node regardless of role) before
  running DDL, re-dialing and self-healing the recorded primary when it's
  stale.

- Fix (M25): the SES verification and SPF TXT records were manually wrapped
  in escaped quotes (`"\"...\""`) — but `aws_route53_record` only wants
  literal `""` to concatenate segments of values longer than 255 characters;
  adding it around a short value doubles the quoting and Route53 rejects it
  ("InvalidCharacterString (Value should be enclosed in quotation marks)").
  Both are now plain text.

- Fix (M25): the 3 SES DKIM CNAME records were folded into the shared `dns`
  module's `for_each` list, keyed by `"type fqdn"` — but the DKIM token (part
  of the CNAME's *name*) is unknown until `aws_ses_domain_dkim` is actually
  created, so `terraform apply` failed immediately with "Invalid for_each
  argument ... will be known only after apply". They're now 3 direct
  `count`-based `aws_route53_record` resources instead (Route53 only).

- Feature (M25, ADR 0025): operator SES SMTP for Keycloak's transactional
  email (password reset, email verification). Opt-in (`mail.outbound_smtp`,
  default off). One sender, `noreply@<root_domain>` — the operator zone
  only, never a tenant domain. Terraform provisions the SES identity, DKIM,
  a custom MAIL FROM domain, and a `ses:SendRawEmail`-only IAM user; the
  derived SMTP password (`internal/aws.DeriveSESSMTPPassword`, AWS's
  published SigV4-based conversion) is mirrored into SSM and reconciled onto
  every tenant realm's SMTP settings. See `docs/mail-smtp.md` — in
  particular, the SES sandbox is a separate, manual blocker even with the
  flag on.

- Fix (configd): `reconcileConfigd`'s tenant roster never set `LogoDataURI`,
  even though `configd.TenantEntry` and the signed `ClientConfig` have
  carried it end to end since this was built — the Cloud-IT VPN desktop app
  has never actually received a tenant's logo. `Primary` made the same trip
  correctly; the logo silently didn't.

- Feature (M24, ADR 0023): per-tenant Keycloak login branding. `branding:`
  gains `secondary` alongside `primary`; the logo (`logo_data_uri`, already
  read by the VPN client) is now also embedded as a CSS `background-image` in
  a rendered per-tenant `login.css` — no separate asset file. `mksrv tenant
  apply` sets `loginTheme` on the realm and restarts Keycloak once. Every
  declared tenant's theme directory is guaranteed to exist before `mksrv
  apply` (re)starts Keycloak (`ensureTenantThemeDirs`), so adding a tenant is
  safe before its branding is ever provisioned. See `docs/branding.md`.

- Feature (M23 phase 4, ADR 0022): alert-rule catalog. `backup.sh` writes a
  `mksrv_backup_last_success_seconds` textfile metric on success. A new
  `prometheus-rules.yml` (4 groups, 11 rules — host pressure, `TargetDown`,
  `PatroniNoLeader`, `OpenBaoSealed`, `RedisMemoryHigh`,
  `PostgresConnectionsSaturated`, `EndpointDown`, `CertExpiringSoon`,
  `BackupStale`) is loaded via Prometheus's `rule_files:`; a rule for an
  undeployed subsystem simply never fires. `fleet-overview` gets a "Firing
  alerts" panel. Notifications are deliberately **not** auto-provisioned —
  see `docs/monitoring.md` for the two-minute manual setup. **M23 is
  complete.**

- Feature (M23 phase 3, ADR 0021): edge visibility. `blackbox_exporter`
  probes every operator + tenant-rest FQDN over HTTPS (reachability +
  certificate expiry via `probe_ssl_earliest_cert_expiry`) — the target list,
  `render.Context.OperatorFQDNs`, mirrors Terraform's `local.operator_fqdns`.
  Keycloak's already-enabled metrics are now published on the private IP too.
  Caddy gets a `servers { metrics }` global option and a private-IP-only
  vhost that proxies just `/metrics` to the (still loopback-only) admin API.
  `monitor` gains the `blackbox`, `keycloak`, `caddy` scrape jobs.

- Feature (M23 phase 2, ADR 0020): per-service exporters. OpenBao's own
  Prometheus telemetry (`telemetry` stanza + unauthenticated `/v1/sys/metrics`
  on the VPC-only listener); `postgres_exporter` alongside every Patroni node
  and `redis_exporter` alongside `cache`, both `Network=host` over `127.0.0.1`
  reusing existing secrets — no change to a live cluster's `pg_hba` needed.
  `monitor`'s Prometheus gains the `openbao`, `postgres-exporter`, and
  `redis-exporter` scrape jobs.

- Feature (M23 phase 1, ADR 0019): fleet-wide metrics. A new `agent` stack
  (node-exporter + cAdvisor, published on each host's private IP) is
  auto-assigned to every host once any host carries `monitor` — no Terraform
  change needed, the intra-VPC security group rule already allows it.
  `monitor`'s Prometheus now scrapes the whole fleet (`render.Context.Fleet`)
  plus Patroni's native `/metrics`, closing the blind spot where only the
  `monitor` host itself had metrics. Grafana gets a provisioned dashboards
  volume and one bundled dashboard, `fleet-overview`. See `docs/monitoring.md`.

- Fix: `infra/root/.terraform.lock.hcl` is now committed and embedded (`assets.go`
  gains an explicit embed for it, since bare `//go:embed infra` skips dotfiles).
  Without it every `mksrv apply` on a `dev` engine re-resolved the AWS provider
  from scratch. The lock pins `hashicorp/aws` with multi-platform hashes.

- Fix (M21): `backup.sh` never initialised the restic repository, so the very
  first run failed with "Is there a repository at the following location?". It
  now runs `restic init` when `restic cat config` shows no repo. It also no
  longer treats restic's exit 3 ("some source files could not be read" —
  sockets, files that changed mid-read) as a failure: the snapshot is written.
- Fix (M21): `mksrv-backup.service` `ExecStart`ed `backup.sh` directly, but the
  script lives under `/var/lib/mksrv` (relabelled `container_file_t` for podman)
  and systemd cannot exec it there — `status=203/EXEC`, Permission denied. Now
  `ExecStart=/usr/bin/bash …`.

- Fix: the Keycloak admin token expires (60s master-realm default) partway
  through a long `mksrv tenant apply`; the client now re-authenticates once on
  a 401 and retries.
- Fix (M13): `bao policy write <name> -` read empty stdin because `baoExec`
  built `podman exec` without `-i`, so `mksrv tenant apply` failed at the
  per-tenant OpenBao step ("'policy' parameter not supplied or empty").
- Fix (M5): `mksrv tenant apply`'s pgAdmin server-list load wrote the file
  inside the container and then `podman cp`'d it from the host, which fails.
  Now `tee`s on the host first.

- Fix (M20): `mksrv postgres bootstrap` recorded `.mksrv/postgres.json`'s
  `primary` as the Patroni IP, but `pgConn` (used by `mksrv tenant apply`) looks
  it up as a fleet host name — `mksrv tenant apply` then failed with
  "postgres primary "10.20.x.x" is not a fleet host". Now records the host name;
  `pgConn` also tolerates an IP from an older file.
- Fix (M11): dedicated-volume bind mounts (`prometheus` tsdb, `loki` chunks,
  `patroni` pgdata/raft, `openbao` baoraft) now use `:Z,U` so podman chowns the
  freshly-formatted XFS mount to the container's user. Without it Prometheus
  (uid 65534), Loki, Patroni and OpenBao exited on "permission denied" writing
  to their root-owned data dir.
- `mksrv deploy` now pulls a stack's images (`podman pull`, retried) before
  starting its units, so a slow first pull can't trip `TimeoutStartSec` /
  the restart rate limiter (which failed Grafana's 547 MB image on a fresh host).
- Fix (M20): the `database` stack's `postgres` TCP health check probed
  `127.0.0.1:5432` on the app host — nothing there in cluster mode. Removed; the
  standalone container gates on its own `pg_isready` HealthCmd and the cluster
  is gated by `mksrv postgres bootstrap`.
- Fix (M20): `pgadmin` / `postgrest` container units hard-coded
  `Requires=`/`After=mksrv-postgres.service`. In cluster mode that unit doesn't
  exist on the app host, so `systemctl restart mksrv-pgadmin.service` failed
  with "Unit mksrv-postgres.service not found". The dependency is now guarded by
  `{{ if not (.StackIP "postgres") }}`.
- Fix: the `cache` stack's `users.acl` seed carried a leading comment block.
  Redis's external `aclfile` parser accepts only `user …` and blank lines, so
  Redis aborted startup ("Aborting Redis startup because of ACL errors … should
  start with user keyword"). The seed template is now comment-free.
- Fix (infra): the `aws-host` module's `openbao_kms` / `backup_s3` IAM policy
  `count` depended on the KMS-key / S3-bucket ARN (unknown until apply), which
  broke the first real `apply` of a fleet with `openbao` assigned. `count` now
  keys off plan-time booleans (`openbao_kms_enabled` / `backup_enabled`).

## v0.1.0 — 2026-09-03

First tagged release. Everything below (M0–M21) ships in `v0.1.0`: the CLI +
embedded Terraform, the stack catalog (`base`, `identity`, `mail`, `database`,
`postgres`, `openbao`, `cache`, `monitor`, `logs`, `security`, `backup`, and the
`files` / `analytics` descriptors), the mesh / configd VPN broker, per-tenant
RBAC (`admin` / `dev` / `apps` / `vpn`), HA Postgres + OpenBao clusters, per-stack
storage + retention, and restic backups. `mksrv-configd` and `mksrv-postgres`
images are pinned to `:v0.1.0`.

## Unreleased — M21

- Added the `backup` stack (ADR 0018): restic → S3, daily on a systemd timer.
  Captures `pg_dump` of every tenant DB from the Patroni primary, an OpenBao
  raft snapshot, Keycloak realm exports, and the stack / podman volumes.
  Terraform creates a versioned, encrypted `mksrv-<env>-backups` bucket and
  grants the backup host's instance role S3 access (no keys — IMDS). The restic
  password is a podman secret; the OpenBao token is a raft-snapshot-only
  periodic token. `mksrv backup run` / `mksrv backup list`; restore is
  documented (`docs/backup.md`). `mksrv tenant apply` writes `backup.env`.

## Unreleased — M20

- `database` runs on the Patroni cluster when a `postgres` cluster is in the
  fleet (ADR 0017), auto-detected from `.mksrv/postgres.json`. Standalone
  Postgres is kept for single-host / `local` dev. `provisionDatabases` targets
  the Patroni primary; PostgREST connects with a libpq multi-host DSN
  (`target_session_attrs=read-write`); pgAdmin registers the primary IP.
- A stack template that renders to whitespace is now dropped
  (`deploy.dropEmpty`), so `postgres.container.tmpl` self-disables when a cluster
  exists.
- `stacks/postgres` `pgdata` volume: 40 GiB @ 4000 IOPS → 20 GiB baseline.

## Unreleased — M19

- Postgres access per RBAC group (ADR 0016), completing the model. `mksrv
  tenant apply` provisions `<id>_app` (SELECT on `app` by default; the dev opens
  writes per table) and `<id>_web` alongside `<id>` / `<id>_anon` / `<id>_auth`.
  The `role` claim is now `<id>_web` (one hardcoded mapper) and an
  `app.pgrst_pre_request` function narrows it — `admin`/`dev` → `<id>`, `apps` →
  `<id>_app`, token-less → `<id>_anon` — from the token's `groups` claim.
  PostgREST gains `PGRST_DB_PRE_REQUEST`.
- On an existing realm, delete the `mksrv-role` protocol mapper once so
  `tenant apply` recreates it with the `_web` value.

## Unreleased — M18

- OpenBao access is now per RBAC group (ADR 0016). `mksrv tenant apply` writes
  two policies per tenant — `tenant-<id>-admin` (full KV control incl. version
  destroy, Transit key rotate) and `tenant-<id>-dev` (read-only KV, read/write
  `kv/tenants/<id>/dev/*`, Transit encrypt/decrypt) — and two OIDC roles bound
  on the `groups` claim: `tenant-<id>-dev` (`dev` or `admin`, the default) and
  `tenant-<id>-admin` (`admin`, `-role=`-selected). The AppRole (services) is
  repointed to `tenant-<id>-dev`. `apps`/`vpn`-only members can no longer log
  into OpenBao. The pre-RBAC `tenant-<id>` policy/role is removed best-effort.

## Unreleased — M17

- Per-tenant RBAC groups `admin` / `dev` / `apps` / `vpn` (ADR 0016, `docs/rbac.md`).
  `mksrv tenant apply` creates the four groups, adds an
  `oidc-group-membership-mapper` (`groups` claim) to the `cloud-it-vpn-desktop`
  and `openbao` clients, and grants the `admin` group Keycloak's fine-grained
  team-management roles (`manage-users` / `query-users` / `query-groups` /
  `view-users`) so a tenant admin runs their own users from the console.
- configd now requires a VPN-enabled group: `/v1/clientconfig` returns 403
  unless the token's `groups` claim contains `vpn`, `dev`, or `admin`.
  `keycloak.RealmSpec.AdminGroup` / `keycloak.ClientSpec.GroupsClaim`.
- OpenBao per-group policies (M18) and the Postgres `<id>_app` role / group-derived
  `role` claim (M19) are follow-ups; until then every authenticated tenant user
  keeps `role: <id>`.

## Unreleased — M16

- `mksrv tenant apply` mirrors each tenant's own Postgres and Redis connection
  secrets into `kv/tenants/<id>/database` and `kv/tenants/<id>/cache` (for
  tenants that consume those stacks and `openbao`), so a team reads its
  credentials with its AppRole/OIDC token instead of via the operator. SSM
  stays mksrv's source of truth; the mirror is only rewritten when the stored
  value has drifted, keeping KV version history quiet.

## Unreleased — M15

- Human login to OpenBao via Keycloak OIDC (ADR 0015 M15 update). For each
  tenant that consumes `openbao`, `mksrv tenant apply` adds a confidential
  `openbao` OIDC client to the tenant's realm (loopback callback
  `http://localhost:8250/oidc/callback`) and a per-tenant `oidc-<id>/` auth
  mount pointed at the realm, with a `tenant-<id>` role bound to the
  `tenant-<id>` policy. `bao login -method=oidc -path=oidc-<id>`.

## Unreleased — M14

- OpenBao Transit engine for PII-column encryption (ADR 0015 M14 update).
  `mksrv openbao bootstrap` now also enables `transit/`. `mksrv tenant apply`
  creates a non-exportable `transit/keys/<id>` (`aes256-gcm96`, 90-day
  auto-rotate) per tenant that consumes `openbao`, and the `tenant-<id>` policy
  gains `transit/{encrypt,decrypt,rewrap,datakey/plaintext}/<id>` (update) and
  `transit/keys/<id>` (read). Apps encrypt PII via the API; the key never leaves
  the cluster.

## Unreleased — M13

- Per-tenant OpenBao (ADR 0015 M13 update). The `openbao` stack is now
  `per_tenant: true` — a tenant opts in by listing `openbao` in its `stacks:`.
  `mksrv tenant apply` reconciles, per consuming tenant: a `tenant-<id>` policy
  scoped to `kv/tenants/<id>/*`, an AppRole bound to it, and its RoleID/SecretID
  in SSM (`/mksrv/<env>/openbao/approle_<id>_{role_id,secret_id}`, SecretID
  written once). The reconcile no-ops until `mksrv openbao bootstrap` has run.

## Unreleased — M12

- Added the `openbao` stack (opt-in, `kind: cluster`, ADR 0015): an OpenBao
  cluster on Integrated Storage (Raft) with **AWS KMS auto-unseal**. Terraform
  creates a fleet KMS key (`alias/mksrv-<env>-openbao`) and grants
  `kms:Encrypt`/`Decrypt` only to hosts carrying the stack; the listener is
  plaintext behind the mesh/VPC (as Patroni). A dedicated `baoraft` gp3 volume
  backs the raft store.
- `mksrv openbao bootstrap` — initialises the cluster, writes the recovery keys
  and root token to SSM (`/mksrv/<env>/openbao/{recovery_keys,root_token}`),
  waits for auto-unseal + a converged raft, and enables **KV v2** and
  **AppRole**. `mksrv openbao status` reports seal/raft/engine state.
- `infra.HostOutput` gains `openbao_kms_key_id`; `render.Context` gains `Region`
  and `OpenBaoKMSKeyID`. Headscale's fleet service-port ACL gains `8200`.
- Per-tenant policies/AppRoles, Transit (PII), PKI, and OIDC-via-Keycloak are
  M13.

## Unreleased — M11

- Per-stack dedicated EBS volumes (ADR 0014): `stack.yaml` `storage: [{name, gb,
  iops, throughput, grows_with}]`. The CLI aggregates a host's volumes, Terraform
  attaches a gp3 volume per entry (provisioned IOPS/throughput when set), and the
  bootstrap (`BootstrapVersion` 10) matches disks by EBS volume id and mounts each
  at `/var/lib/mksrv/vol/<name>`. Templates use `{{ .Volume "<name>" }}`.
  `postgres`, `monitor`, and `logs` adopt it.
- `deployment.yaml` `retention: {metrics_days, logs_days, metrics_gb_per_day,
  logs_gb_per_day}` — feeds the Prometheus / Loki retention flags (were
  hardcoded) and the `grows_with` volume sizing.
- `mksrv host migrate-volume <host> <stack> [name...]` — copies a stack's
  named-volume data onto its dedicated EBS volume for an existing deployment.

## Unreleased — M10

- Capacity checks: `mksrv validate` now emits a `capacity.overcommit` **warning**
  (validity unaffected) when an AWS host's stacks' `resources.min_ram_mb` sum
  exceeds the instance memory. `internal/model` gains `InstanceRAMMB` (a table of
  the t/m/c/r families) and `SwapForStacks`.
- Adaptive swap: swap moves from first-boot Terraform (`swap_mb`, never
  reconciled) to the re-runnable bootstrap script (`BootstrapVersion` 9). The
  size is derived per host from `sum(min_ram_mb) + 512 headroom − instance RAM`,
  rounded to 512, capped at `min(RAM, 4 GiB)`. `aws_instance` sets
  `user_data_replace_on_change = false` so the swap removal is not a replacement.
- New `docs/capacity.md` — the sizing model and a per-instance-type table for the
  distributed profile.

## Unreleased — M9

- Added `kind: cluster` stacks (ADR 0013): a stack that must be assigned to an
  odd number of hosts ≥ 3 and self-organises a quorum. `render.Context` gains
  `StackMembers` / `.StackNodes` / `.StackPeers`.
- Added the `postgres` stack (opt-in): Patroni-managed HA PostgreSQL 16 with a
  raft DCS (no etcd), automatic failover, image `ghcr.io/<owner>/mksrv-postgres`
  built in CI. `mksrv postgres bootstrap` waits for the cluster, records
  `.mksrv/postgres.json`, creates the `app` database, and can force a switchover.
- `infra/modules/network` gains `subnet_count` (multi-AZ subnets, `moved` blocks
  keep the live single subnet). The CLI sets it to 3 when a `kind: cluster` stack
  is assigned. AWS hosts round-robin across the AZ subnets.

## Unreleased — M8

- Added the `logs` stack (Grafana Loki + Alloy journal collector) and the
  `security` stack (CrowdSec engine + `cs-firewall-bouncer`), both opt-in
  (ADR 0012). Loki uses filesystem storage and label-based tenant scoping;
  CrowdSec is CAPI-enrolled and enforces bans via its own nftables tables.
- `stacks/base` Caddyfile now emits a JSON access log (for Loki and CrowdSec).
- `render.Context` gains `StackHosts` + `StackIP(name)` — the private IP of the
  fleet host carrying a stack, for cross-host templates.
- Bootstrap `BootstrapVersion` 8: podman `log_driver = "journald"` + persistent
  journal, so all container stdout is collectable from the journal.
- Fix: `deploy.DeployStack` now exposes resolved stack secrets to `shared`
  templates as `{{ .Secrets.<leaf> }}` (previously only per-tenant reconcilers
  set `Context.Secrets`).

## Unreleased — M7

- Tenant-owned infrastructure (ADR 0011). Three optional tenant-document blocks:
  - `forwards` — per-tenant Cloud-IT VPN forwards (`http`/`tcp`/`ssh`), appended
    to the configd roster after the built-in fleet forwards. No `configd` or
    `cloud-it-vpn` change.
  - `dns` — A/AAAA/CNAME records written into the tenant's own Route53 zone
    (`dns_override.zone_id`) via a new `module "dns_tenant"`, with
    `allow_overwrite = false` so mksrv never clobbers a record it did not create.
  - `mesh_routes` — CIDRs a tenant's nodes may advertise; each gets a Headscale
    ACL rule (route approval stays manual).
- `mksrv tenant mesh <id>` — mints a Headscale pre-auth key under the tenant's
  Headscale user and prints the `tailscale up` command for a tenant-owned node.
- `headscale.Policy` now takes `[]PolicyTenant` (id + routes) instead of `[]string`.

## Unreleased — M6

- Added the `cache` stack: one shared Redis 7 on the data host, with a
  per-tenant ACL login user confined to the `<id>:*` key and channel namespace
  (no `@dangerous`/`@admin` commands). `mksrv tenant apply` rewrites the ACL
  file and runs `ACL LOAD`; passwords land in SSM
  `/mksrv/<env>/cache/redis_<id>_password`. Reachable over the tailnet and via a
  `cache` forward in the configd roster; Headscale ACL opens port 6379.
- Added PostgREST to the `database` stack: `mksrv tenant apply` now deploys one
  `mksrv-postgrest-<id>` container per tenant that consumes `database`, connected
  as an `<id>_auth` authenticator role that switches into `<id>` (JWT `role`
  claim) or `<id>_anon`. Tokens are verified against the tenant realm's JWKS; a
  hard-coded `role` claim mapper is added to the `cloud-it-vpn-desktop` client.
  Each instance is exposed at `<id>.rest.<root_domain>` (edge Caddy vhost + an
  operator DNS record) and over the tailnet, with a `rest` forward in the
  configd roster. Headscale ACL opens ports 3010-3019.
- Added `mksrv destroy --infra-only` (confirmation-gated `terraform destroy`).
- Added a GitHub Actions workflow that builds and pushes a multi-arch
  `ghcr.io/<owner>/mksrv-configd` image; the `identity` stack now references it.
- `internal/keycloak` gains `EnsureClient` (single-client reconcile returning
  the client secret).
- Added a Spanish quick-start (`docs/quickstart.es.md`).

## Unreleased — M5

- Added the `database` stack (PostgreSQL 16 + pgAdmin) and the `monitor` stack
  (Prometheus, Grafana, node-exporter, cAdvisor). `mksrv tenant apply` now
  provisions a database, login role, and `app` schema per tenant.
- Cross-host Caddy vhosts: rendered files under `/var/lib/mksrv/caddy.d/` from a
  non-edge host's stack are written to the edge and Caddy is reloaded.
- `render.Context` gains `Peers` (fleet private IPs), `Host.TailnetIP`;
  `mksrv mesh` writes `.mksrv/mesh.json`. Caddy admin moves to `127.0.0.1:2019`.
- `aws-host` gets an intra-VPC ingress rule; two operator DNS records
  (`grafana.`, `pgadmin.`).

## Unreleased — M4

- Added `internal/keycloak` (Admin REST client — realms, groups, OIDC clients,
  declarative users) and `internal/configd` + `cmd/configd` (the Cloud-IT VPN
  broker: verifies a Keycloak RS256 token, mints a one-use Headscale pre-auth
  key, returns a compact Ed25519 JWS clientconfig). Containerfile + goreleaser
  `configd` binary.
- New commands `mksrv tenant apply` (realms, groups, clients, mesh users, and
  the configd unit + signing key + roster) and `mksrv users apply`.
- `internal/secrets` gains `Put` / `EnsureString`; `internal/headscale` gains
  `CreateAPIKey`; stack deploy runs `podman network reload --all` after
  container restarts.
- The `identity` `configd` app now points at `localhost/mksrv-configd:dev`; the
  Caddy fragment serves the `cfg.` vhost.

## Unreleased — M3

- Added `internal/secrets` (SSM Parameter Store resolver; `EnsureRandom`
  generates a SecureString on first use) and `internal/headscale` (users and
  pre-auth keys via the container CLI).
- `internal/deploy` gains podman-secret push, `post_deploy` hook execution,
  per-stack `.deployed` markers, and `mesh.go` (tailnet node join). Bootstrap v7:
  relocated-graphroot SELinux equivalence, `tun` + netfilter modules.
- Added the `identity` stack: `mksrv-identity` network, a dedicated Postgres,
  Keycloak, Headscale, a Caddy vhost fragment, and a reload hook. Caddy moves to
  host networking and imports `caddy.d/*.caddy` fragments.
- New command `mksrv mesh`: reconciles Headscale users (one per tenant plus a
  fleet user) and joins every fleet host to the tailnet.
- `scripts/public-hygiene.sh` allows well-known public DNS resolvers and the
  RFC 6598 (Tailscale CGNAT) range.

## Unreleased — M2

- Added `internal/ssh` (SSH + SFTP transport, workspace-pinned known_hosts,
  explicit first-use enrollment), `internal/render` (text/template stack
  renderer), and `internal/deploy` (idempotent Rocky 9 bootstrap + checksum-aware
  stack deploy with Quadlet activation and health checks).
- Added the `base` stack templates (Caddy Quadlet + Caddyfile + podman network).
- New commands: `mksrv host trust`, `mksrv bootstrap`, `mksrv deploy`,
  `mksrv status`. `mksrv apply` without `--infra-only` now bootstraps and
  deploys the fleet (base host first); `--trust-hosts` auto-enrolls on first run.

## Unreleased — M1

- Added `mksrv plan --infra-only` and `mksrv apply --infra-only`: resolve AWS
  credentials, bootstrap the S3 + DynamoDB Terraform state backend
  (`internal/aws`), decode the workspace into Terraform variables
  (`internal/infra`), and run the Terraform root. `apply` writes
  `.mksrv/infra/outputs.json` and updates `mksrv.lock`.
- Filled the Terraform modules: `network` (dedicated single-subnet VPC, no NAT),
  `aws-host` (Rocky 9 arm64 EC2, gp3 data volume, EIP, locked-down security
  group, SSM instance profile, minimal cloud-init), `dns` (Route 53 records),
  and `infra/root` wiring (hosts + operator-zone DNS).
- `internal/aws` exports the resolved credentials as `AWS_*` for Terraform, so
  credential sources newer than Terraform's embedded SDK work.
- `scripts/public-hygiene.sh`: no longer flags templated/service ARNs or HCL
  expression references in Terraform; allows the public Rocky AMI owner id.
- Added `mksrv init`: scaffolds a private workspace (`deployment.yaml`, `tenants/`,
  `.gitignore`, `.mksrv/`, `README.md`) from embedded templates, with flags or
  interactive prompts, `--force`, `--json`, and a validation pass on the result.
  New `internal/scaffold` package. ADR 0009.
- Added an optional per-tenant `mail` block (`domains`, `inbound`, `dmarc_rua`) to
  `schemas/tenant.v1.json` and `model.Tenant`, to drive per-domain SES identity in
  the `mail` stack.
- Recorded ADR 0010: operator domain plus per-tenant DNS overrides, the
  edge/data host split, SSM+age secrets, and a minimal `configd`.
- Added `internal/tf`: pinned Terraform version (`1.9.8`), binary location and
  download via `hashicorp/hc-install`, and a `Runner` over
  `hashicorp/terraform-exec` (`Init`, `Validate`, `Plan`, `Apply`, `Output`).
  ADR 0008.
- `mksrv version` now reports the real pinned Terraform version.
- Recorded ADR 0009: `mksrv init` will generate a private workspace scaffold.
- Raised the Go baseline to 1.25 (required by current `hc-install`); CI matrix is
  now `1.25.x` and `staticcheck` is `v0.8.1`.

## Unreleased — M0

- Added public/private engine-workspace boundary and repository scaffolding.
- Added embedded engine cache and seven stack descriptors.
- Added four JSON Schemas and schema-plus-semantic workspace validation.
- Added `version`, `validate`, and `doctor` with JSON output.
- Added synthetic examples, tests, release automation, and public-data hygiene.
- Replaced the temporary offline adapters with Cobra, `sigs.k8s.io/yaml`, and
  `santhosh-tekuri/jsonschema/v6`; behavioral tests unchanged (ADR 0007).
- Embedded `time/tzdata` so `-trimpath` builds validate timezones without
  system zoneinfo.
