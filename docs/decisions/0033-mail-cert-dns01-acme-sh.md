# ADR 0033: The shared mail server's certificate comes from acme.sh (DNS-01), not Caddy

- Status: Accepted
- Date: 2026-10-02
- Milestone: M33
- Refines: ADR 0032 (tenant mail hosting)

## Context

ADR 0032's `mail.branded_hostname` publishes `mail.<tenant-domain>` so a
hosted tenant's users can configure their own domain in their mail client
instead of the operator's. The implementation added every branded hostname
as an extra address on the shared mail server's Caddy site block
(`mail.<root_domain>, mail.<branded-1>, mail.<branded-2> { ... }`), on the
documented assumption that Caddy would issue one certificate covering every
hostname in that address list, and `reconcileMailTLS` copied that one
certificate into docker-mailserver's manual-TLS mount.

That assumption was wrong, and it ran live before anyone noticed: with one
branded tenant (`mcps`, `mail.mcps-epcm.org`), Caddy obtained and kept
renewing **two independent single-SAN certificates** — one for
`mail.cloud-it.click`, one for `mail.mcps-epcm.org` — never a combined one.
Confirmed with a Caddy maintainer (Francis Lavoie, Caddy community forum):
"Caddy does not support multi-SAN certificates, for a multitude of reasons."
Since `docker-mailserver` in `SSL_TYPE=manual` loads exactly one static
cert/key pair for every TLS connection regardless of SNI, and its own docs
confirm per-domain SNI certificate selection isn't supported either, whichever
of the two certs `reconcileMailTLS` happened to copy in satisfied only one of
the two hostnames — the other's mail client saw a hostname/cert mismatch and
failed the TLS handshake outright. Reproduced live: `mail.mcps-epcm.org`-
configured clients inside the `mcps` network got `SSL_accept() failed ...
certificate unknown` on every connection attempt, while `mail.cloud-it.click`
clients worked the whole time — the bug was invisible unless you specifically
tested the branded hostname.

Neither side of the existing toolchain can be configured around this:
Caddy's own automatic HTTPS has no multi-SAN mode, and docker-mailserver has
no documented multi-cert/SNI mode either — each expects the *other* layer to
solve it. Something has to obtain the actual multi-SAN certificate; Let's
Encrypt supports that natively (DNS-01 or HTTP-01 against one order with
several identifiers), it's specifically Caddy's own issuance path that
refuses to produce it.

## Decision

Obtain the shared mail server's certificate directly via ACME DNS-01,
bypassing Caddy entirely for this one certificate. Caddy keeps managing every
other hostname in the fleet exactly as before — this is scoped to the one
cert docker-mailserver loads.

- **`acme.sh`** (`docker.io/neilpang/acme.sh:3.1.6`, pinned), run via
  `podman run --rm --network host`, same shape as `restic`/`postgres`/`bao`
  one-off containers in `backup.sh.tmpl`. Not a Go ACME client: DNS-01 against
  Route53, SigV4-signing, retry/propagation-wait, and renewal bookkeeping are
  all solved problems in a widely-used, independently maintained tool; a
  hand-rolled Go DNS-01 client would re-solve all of that for one cert.
- **DNS-01 via `dns_aws`**, credentials from **short-lived IMDSv2 instance-role
  keys** fetched by the wrapping script — never a static key on disk. New
  Terraform variable `mail_cert_zone_arns` (aws-host module) grants the host
  carrying `mail` exactly `route53:ChangeResourceRecordSets` /
  `ListResourceRecordSets` on the operator zone plus every mail-hosted,
  branded tenant's own zone (`route53:GetChange` has no zone-scoped resource
  type, so that one action is necessarily `Resource: "*"` — it is a read of an
  async change's status, not a write). Computed in `infra/root/main.tf`
  (`mail_cert_zone_arns`), mirroring how `dns_tenant` already iterates
  per-tenant zone ids for DNS records.
- **`mailCertHostnames`** (`internal/cli/mail_hosting.go`) replaces the
  `MailBrandedHostnames` render-context field: `mail.<root_domain>` first (the
  name `--install-cert` looks the issued cert up by), then `mail.<domain>` for
  every domain of every `hosted && branded_hostname` tenant, sorted. A tenant
  with more than one mail domain gets a SAN per domain, not just one.
- **`reconcileMailCert`** replaces `reconcileMailTLS`: writes the rendered
  issuance script + a daily `mksrv-mail-cert.timer`/`.service` pair (same
  shape as `mksrv-backup.timer`), and — unlike the timer, which just waits for
  its next tick — runs the script immediately, with `--force`, whenever the
  SAN list itself changed since the last run, so a tenant that just turned on
  `branded_hostname` doesn't wait up to a day for its first working cert. The
  mail server restarts only if the resulting `fullchain.pem` actually changed,
  same idiom as every other reconcile function in this file.
- **Caddy's role in this drops to zero.** `mail.caddy.tmpl` is deleted (it
  existed purely to make Caddy request a cert for mksrv to copy); the
  `MailBrandedHostnames` render-context field and its computation in
  `hosts.go` are removed with it. `docker-mailserver`'s `SSL_CERT_PATH`/
  `SSL_KEY_PATH` env vars and the `/tls` bind mount are unchanged — only who
  writes `fullchain.pem`/`privkey.pem` there changed.

## Consequences

### Positive

- The actual bug — branded hostnames failing TLS — is fixed by construction:
  there is now exactly one certificate, with every hostname as a real SAN,
  the thing `docker-mailserver`'s manual TLS mode has always expected.
- Renewal is now a genuine background process (the timer), not something that
  only happens to run when an operator next runs `mksrv tenant apply` —
  matching how Caddy's own certificates already renew continuously.
- No static AWS credential anywhere for this — IMDSv2 instance-role keys,
  scoped by Terraform to only the zones this specific certificate's DNS-01
  challenge needs to write into.
- `mailCertHostnames` is pure and directly testable (`TestMailCertHostnames`),
  unlike the old path, whose only coverage was asserting on a rendered Caddy
  fragment string.

### Negative

- A second ACME client in the fleet (acme.sh, alongside Caddy's own) —
  another moving part, another image to keep pinned and patched, another
  place credential/renewal failures can hide. Accepted: the alternative is
  either leaving branded hostnames broken, or dropping the feature.
- `acme.sh`'s own state (account registration, per-domain order bookkeeping)
  lives in `/var/lib/mksrv/stacks/mail/acme-state`, a new piece of state
  `restic` doesn't know to back up yet (today's `backup` stack only collects
  Postgres/OpenBao/Keycloak dumps and the storage-module volumes it's told
  about). Not urgent — a lost ACME account/order history just means the next
  `--issue` registers a fresh account and reissues; nothing user-facing is
  lost — but worth closing before this matters for an unrelated reason
  (`backup.sh.tmpl` growing a `mksrv-mail-acme-state` volume entry).
- `GetChange`'s `Resource: "*"` is the broadest grant in this ADR. Scoped as
  tightly as the action allows: Route53 doesn't offer a narrower resource type
  for it, and it is read-only (poll the status of a change that was itself
  already authorized by the `ChangeResourceRecordSets` grant).

## Alternatives discarded

- **A multi-SAN-capable Go ACME library** (e.g. `lego`) instead of shelling
  out to `acme.sh`. Would keep mksrv a single static binary with no new image
  dependency, but re-implements DNS-01 propagation handling, retry/backoff,
  and ACME account/order persistence — all things `acme.sh` has already
  solved and this fleet doesn't otherwise need a general-purpose ACME library
  for. Revisit if a second, unrelated need for in-process ACME shows up.
- **Custom Postfix `smtpd_tls_sni_maps` / Dovecot `local_name` overrides**
  inside `docker-mailserver`, keeping Caddy's two independently-issued certs
  and selecting between them by SNI instead of requesting one combined cert.
  Technically the standard way *other* multi-domain mail setups solve this —
  but `docker-mailserver` itself documents multi-domain SSL as unsupported
  (its answer is "use one cert with every domain as a SAN"), so this would
  mean fighting its entrypoint/config-generation scripts to inject overrides
  they don't expect, on every image upgrade. Getting the one cert
  `docker-mailserver` already wants is less fragile than making it do
  something it says it can't.
- **Drop `branded_hostname` for mail** (the stopgap while this ADR was being
  written): simplest possible fix, zero new code, but reneges on what
  `mcps`'s users were already told to configure. Superseded by this ADR.
