// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	awsclient "github.com/fenandosr/mksrv/internal/aws"
	"github.com/fenandosr/mksrv/internal/infra"
	"github.com/fenandosr/mksrv/internal/model"
	secretsx "github.com/fenandosr/mksrv/internal/secrets"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

const (
	mailConfigPath     = "/var/lib/mksrv/stacks/mail/config/postfix-accounts.cf"
	mailVirtualPath    = "/var/lib/mksrv/stacks/mail/config/postfix-virtual.cf"
	mailTLSDir         = "/var/lib/mksrv/stacks/mail/tls"
	mailRelayMapPath   = "/var/lib/mksrv/stacks/mail/config/postfix-relaymap.cf"
	mailSASLPasswdPath = "/var/lib/mksrv/stacks/mail/config/postfix-sasl-password.cf"
)

var mailLocalPartRe = regexp.MustCompile(`[^a-z0-9]+`)

func mailLocalPartKey(address string) string {
	return mailLocalPartRe.ReplaceAllString(strings.ToLower(model.TenantMailbox{Address: address}.MailLocalPart()), "_")
}

// mailPasswordRef is the SSM path for one mailbox's generated password.
func mailPasswordRef(tenantID, address string) string {
	return "/mksrv/{env}/mail/tenant_" + tenantID + "_" + mailLocalPartKey(address) + "_password"
}

// mailPasswordHashRef is the SSM path for a *pre-computed* password hash for
// one mailbox — an operator migrating real users off another mail server can
// seed this directly (`aws ssm put-parameter --type SecureString`) with the
// exact `{SHA512-CRYPT}$6$...` line already in that server's own
// postfix-accounts.cf (the format docker-mailserver uses is portable between
// installations — salted, no per-install secret involved) so the mailbox
// keeps working with the user's existing password instead of forcing a
// reset. Checked before mailPasswordRef/EnsureRandom; only ever read here,
// mksrv never writes to it.
func mailPasswordHashRef(tenantID, address string) string {
	return "/mksrv/{env}/mail/tenant_" + tenantID + "_" + mailLocalPartKey(address) + "_password_hash"
}

// mailHost returns the fleet host carrying the `mail` stack, or nil.
func (f *fleet) mailHost() *hostTarget {
	for i := range f.targets {
		if slices.Contains(f.targets[i].Host.Stacks, "mail") {
			return &f.targets[i]
		}
	}
	return nil
}

// tenantMailHosted is the one gate for "is this tenant live on the shared
// mail stack" — `mail.hosted: true` with at least one domain. `hosted: false`
// (or unset) means the `mail:` block is just documentation — e.g.
// forward-looking SPF/DMARC intent — not live yet. Every mail-provisioning
// path (the shared mailbox file, DKIM generation, …) must gate on this, not
// just `t.Mail != nil`, or a tenant with a documentation-only `mail:` block
// gets provisioned anyway.
func tenantMailHosted(t model.Tenant) bool {
	return t.Mail != nil && t.Mail.Hosted && len(t.Mail.Domains) > 0
}

// mailTenants returns every tenant opted into the shared mail stack, sorted
// by id — the full set, not just this run's `--tenant` selection, so a
// partial `tenant apply <id>` never drops another tenant's mailboxes from the
// shared accounts file.
func mailTenants(tenants map[string]model.Tenant) []string {
	var ids []string
	for id, t := range tenants {
		if tenantMailHosted(t) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// provisionMail reconciles the shared mail server (ADR 0032): the mailbox
// account file (every mail-consuming tenant, not just `selected`), the DKIM
// key + TXT record for each selected tenant's domains, and the TLS cert
// copied in from Caddy. No-ops when no host carries `mail` or no tenant has a
// `mail:` block.
func (f *fleet) provisionMail(ctx context.Context, printer ui.Printer, selected []string) error {
	host := f.mailHost()
	all := mailTenants(f.data.Tenants)
	if host == nil || len(all) == 0 {
		return nil
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}

	client, err := sshx.Dial(ctx, host.Target, f.knownHosts)
	if err != nil {
		return dialError(host.Name, err)
	}
	defer client.Close()

	restarted, err := f.reconcileMailboxes(ctx, printer, client, all)
	if err != nil {
		return err
	}
	if err := f.reconcileMailAliases(ctx, printer, client, all, restarted); err != nil {
		return err
	}
	if err := f.reconcileMailCert(ctx, printer, client); err != nil {
		return err
	}
	if err := f.reconcileMailRelay(ctx, printer, client); err != nil {
		return err
	}
	for _, id := range selected {
		t, ok := f.data.Tenants[id]
		if !ok || !tenantMailHosted(t) {
			continue
		}
		if err := f.reconcileDKIM(ctx, printer, client, id, t); err != nil {
			return err
		}
	}
	return nil
}

// reconcileMailboxes writes postfix-accounts.cf for every mailbox across every
// mail-consuming tenant and restarts the server only if it changed. It reports
// whether it restarted the server, so the alias reconcile that follows does not
// restart it a second time in the same run.
func (f *fleet) reconcileMailboxes(ctx context.Context, printer ui.Printer, client *sshx.Client, tenantIDs []string) (restarted bool, err error) {
	type line struct {
		address, hash string
	}
	var lines []line
	for _, id := range tenantIDs {
		t := f.data.Tenants[id]
		for _, mb := range t.Mail.Mailboxes {
			hash, err := f.resolver.Get(ctx, mailPasswordHashRef(id, mb.Address))
			if err != nil {
				if !secretsx.IsNotFound(err) {
					return false, fmt.Errorf("mail %s: password hash for %s: %w", id, mb.Address, err)
				}
				// No pre-seeded hash (the common case: a brand-new mailbox,
				// not a migrated one) — generate and hash a fresh password.
				pass, passErr := f.resolver.EnsureRandom(ctx, mailPasswordRef(id, mb.Address), 24)
				if passErr != nil {
					return false, fmt.Errorf("mail %s: password for %s: %w", id, mb.Address, passErr)
				}
				res, hashErr := client.RunInput(ctx, "openssl passwd -6 -stdin", []byte(pass))
				if hashErr != nil {
					return false, fmt.Errorf("mail %s: hash password for %s: %w", id, mb.Address, hashErr)
				}
				hash = strings.TrimSpace(res.Stdout)
			}
			// else: a pre-computed hash was seeded directly (migrated
			// mailbox) — the user's existing password keeps working, nothing
			// generated or stored.
			lines = append(lines, line{strings.ToLower(mb.Address), hash})
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].address < lines[j].address })

	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%s|%s\n", l.address, l.hash)
	}
	content := b.String()

	old, _ := client.Run(ctx, "sudo cat "+mailConfigPath+" 2>/dev/null || true")
	if old.Stdout == content {
		return false, nil
	}
	if err := client.WriteFileSudo(ctx, mailConfigPath, []byte(content), 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", mailConfigPath, err)
	}
	if _, err := client.Run(ctx, "sudo systemctl restart mksrv-mailserver.service"); err != nil {
		return false, fmt.Errorf("restart mailserver: %w", err)
	}
	printer.Success("mail: %d mailbox(es) across %d tenant(s)", len(lines), len(tenantIDs))
	return true, nil
}

// mailAliasFileHeader marks the file as owned by mksrv: a hand-made alias added
// on the server is overwritten at the next `tenant apply`.
const mailAliasFileHeader = "# Managed by mksrv (tenants' mail.aliases, ADR 0032 addendum). Manual edits are overwritten.\n"

// mailAliasFile renders postfix-virtual.cf for every alias of every
// mail-consuming tenant: `alias@domain target1 target2`, lower-cased, sorted by
// alias. Empty when no tenant declares an alias.
func mailAliasFile(tenants map[string]model.Tenant, tenantIDs []string) string {
	var lines []string
	for _, id := range tenantIDs {
		for _, al := range tenants[id].Mail.Aliases {
			to := make([]string, 0, len(al.To))
			for _, t := range al.To {
				to = append(to, strings.ToLower(strings.TrimSpace(t)))
			}
			sort.Strings(to)
			lines = append(lines, strings.ToLower(strings.TrimSpace(al.Address))+" "+strings.Join(to, " "))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	sort.Strings(lines)
	return mailAliasFileHeader + strings.Join(lines, "\n") + "\n"
}

// reconcileMailAliases writes postfix-virtual.cf (ADR 0032 addendum) and restarts
// the shared server only when the file changed AND the mailbox reconcile did not
// already restart it in this run. With no aliases anywhere and no file on the
// server it does nothing; once aliases have existed, removing them all rewrites
// the file with only its header so the old ones stop delivering.
func (f *fleet) reconcileMailAliases(ctx context.Context, printer ui.Printer, client *sshx.Client, tenantIDs []string, alreadyRestarted bool) error {
	content := mailAliasFile(f.data.Tenants, tenantIDs)
	old, _ := client.Run(ctx, "sudo cat "+mailVirtualPath+" 2>/dev/null || true")
	if content == "" {
		if strings.TrimSpace(old.Stdout) == "" {
			return nil // never had aliases: nothing to create
		}
		content = mailAliasFileHeader // all aliases removed
	}
	if old.Stdout == content {
		return nil
	}
	if err := client.WriteFileSudo(ctx, mailVirtualPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", mailVirtualPath, err)
	}
	if !alreadyRestarted {
		if _, err := client.Run(ctx, "sudo systemctl restart mksrv-mailserver.service"); err != nil {
			return fmt.Errorf("restart mailserver: %w", err)
		}
	}
	printer.Success("mail: %d alias(es) across %d tenant(s)", strings.Count(content, "\n")-strings.Count(mailAliasFileHeader, "\n"), len(tenantIDs))
	return nil
}

const (
	mailAcmeStateDir = "/var/lib/mksrv/stacks/mail/acme-state"
	mailCertScript   = "/var/lib/mksrv/stacks/mail/mail-cert-issue.sh"
	mailCertService  = "/etc/systemd/system/mksrv-mail-cert.service"
	mailCertTimer    = "/etc/systemd/system/mksrv-mail-cert.timer"
	mailAcmeShImage  = "docker.io/neilpang/acme.sh:3.1.6"
)

// mailCertHostnames is the shared mail server's full certificate SAN list:
// mail.<root_domain> first (the primary name acme.sh issues/installs under),
// then "mail.<domain>" for every domain of every mail-hosted tenant that
// opted into branded_hostname — sorted, one entry per domain (a tenant can
// have more than one). Caddy cannot issue this cert itself: "Caddy does not
// support multi-SAN certificates, for a multitude of reasons" (Francis
// Lavoie, a Caddy maintainer — confirmed live too: the Caddyfile's
// `mail.<root>, mail.<branded> { ... }` site block, which looked like exactly
// the standard "share a cert across these hostnames" pattern, in fact made
// Caddy obtain and renew TWO independent single-SAN certs). See ADR 0033.
func mailCertHostnames(rootDomain string, tenants map[string]model.Tenant) []string {
	hostnames := []string{"mail." + rootDomain}
	var branded []string
	for _, id := range sortedTenantIDs(tenants) {
		t := tenants[id]
		if !tenantMailHosted(t) || !t.Mail.BrandedHostname {
			continue
		}
		for _, d := range t.Mail.Domains {
			branded = append(branded, "mail."+d)
		}
	}
	sort.Strings(branded)
	return append(hostnames, branded...)
}

// mailCertIssueScript renders the script that obtains (and, on a later run,
// renews) the shared mail server's certificate directly via ACME DNS-01 —
// bypassing Caddy entirely, since it cannot produce the multi-SAN cert this
// needs (mailCertHostnames). Idempotent: acme.sh's own `--issue` skips
// reissuing a cert that isn't yet due for renewal, unless the SAN list
// changed, in which case ignoreDueDate forces one anyway — a tenant that just
// turned on branded_hostname shouldn't have to wait for the next scheduled
// run. Credentials: a short-lived IMDSv2 token exchanged for the instance
// role's temporary keys (`mail_cert_route53` on the Terraform side, scoped to
// exactly the zones this needs) — never a static key on disk, matching how
// restic already reaches S3 in backup.sh.tmpl.
func mailCertIssueScript(hostnames []string) string {
	var domainArgs strings.Builder
	for _, h := range hostnames {
		fmt.Fprintf(&domainArgs, " -d %s", h)
	}
	primary := hostnames[0]
	return fmt.Sprintf(`#!/usr/bin/env bash
# mksrv shared mail server certificate — rendered by reconcileMailCert, run by
# mksrv-mail-cert.timer (daily) and once immediately whenever the SAN list
# below changes. DNS-01 via Route53, credentials from the instance role —
# Caddy cannot produce this cert itself (ADR 0033).
set -euo pipefail

STATE=%[1]s
TLS_DIR=%[2]s
IMAGE=%[3]s
FORCE=${1:-}

mkdir -p "$STATE" "$TLS_DIR"

TOKEN=$(curl -sX PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600")
ROLE=$(curl -s -H "X-aws-ec2-metadata-token: $TOKEN" http://169.254.169.254/latest/meta-data/iam/security-credentials/)
CREDS=$(curl -s -H "X-aws-ec2-metadata-token: $TOKEN" "http://169.254.169.254/latest/meta-data/iam/security-credentials/$ROLE")
export AWS_ACCESS_KEY_ID=$(echo "$CREDS" | jq -r .AccessKeyId)
export AWS_SECRET_ACCESS_KEY=$(echo "$CREDS" | jq -r .SecretAccessKey)
export AWS_SESSION_TOKEN=$(echo "$CREDS" | jq -r .Token)

acme() {
	podman run --rm --network host \
		-e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_SESSION_TOKEN \
		-v "$STATE":/acme.sh:Z \
		"$IMAGE" "$@"
}

ISSUE_ARGS=(--issue --server letsencrypt --dns dns_aws%[4]s)
if [ "$FORCE" = "--force" ]; then
	ISSUE_ARGS+=(--force)
fi
# acme.sh --issue exits 2 ("renewal skipped") when the certificate is not due yet. That is the
# normal answer on almost every daily run, not a failure: under set -e it used to abort the
# script and mark the unit failed every day, which hid real failures. Any other status is still
# fatal. The install below then runs every time, as the comment further down already assumed.
rc=0
acme "${ISSUE_ARGS[@]}" || rc=$?
if [ "$rc" -ne 0 ] && [ "$rc" -ne 2 ]; then
	echo "acme.sh --issue failed with status $rc" >&2
	exit "$rc"
fi

# --install-cert alone, with its own TLS_DIR mount -- :z (shared, lowercase)
# because mksrv-mailserver.container also bind-mounts this same host
# directory (:Z, exclusive): relabeling it from here with :Z too would steal
# that container's access to its own already-working cert on every run of
# this script, including the daily timer tick when nothing was actually
# renewed. acme() above never touches TLS_DIR, so a no-op --issue check
# never relabels it at all.
podman run --rm --network host \
	-v "$STATE":/acme.sh:Z \
	-v "$TLS_DIR":"$TLS_DIR":z \
	"$IMAGE" --install-cert -d %[5]s \
	--cert-file "$TLS_DIR/cert.pem" \
	--key-file "$TLS_DIR/privkey.pem" \
	--fullchain-file "$TLS_DIR/fullchain.pem"
`, mailAcmeStateDir, mailTLSDir, mailAcmeShImage, domainArgs.String(), primary)
}

const mailCertServiceUnit = `[Unit]
Description=mksrv shared mail server certificate (acme.sh, DNS-01)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/bin/bash ` + mailCertScript + `
TimeoutStartSec=600
`

const mailCertTimerUnit = `[Unit]
Description=mksrv shared mail server certificate renewal (daily)

[Timer]
OnCalendar=*-*-* 05:00:00
RandomizedDelaySec=1h
Persistent=true

[Install]
WantedBy=timers.target
`

// certSANs parses the DNS names out of `openssl x509 -noout -ext subjectAltName` output, for
// example "X509v3 Subject Alternative Name: \n    DNS:mail.a.example, DNS:mail.b.example".
func certSANs(out string) []string {
	var names []string
	for _, part := range strings.FieldsFunc(out, func(r rune) bool { return r == ',' || r == '\n' }) {
		part = strings.TrimSpace(part)
		if name, ok := strings.CutPrefix(part, "DNS:"); ok && name != "" {
			names = append(names, strings.ToLower(name))
		}
	}
	return names
}

// sanSetChanged reports whether the installed certificate's names differ from the wanted ones
// (as sets, ignoring order and case). When the certificate's names could not be read at all
// (no certificate yet, no openssl), it reports no change: an absent certificate is handled by
// the caller, and an unreadable one must not trigger a forced reissuance.
func sanSetChanged(installed, wanted []string) bool {
	if len(installed) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, n := range installed {
		have[strings.ToLower(n)] = true
	}
	want := map[string]bool{}
	for _, n := range wanted {
		want[strings.ToLower(n)] = true
	}
	if len(have) != len(want) {
		return true
	}
	for n := range want {
		if !have[n] {
			return true
		}
	}
	return false
}

// mailCertDecision says whether to run the issuance script now, and whether to force it. A missing
// certificate is issued normally; a certificate that does not cover the wanted names is reissued
// by force (a tenant that just turned on a branded hostname should not wait for the next tick);
// anything else is left to the daily timer. It deliberately takes no part of the script text.
func mailCertDecision(installedCert, sanOutput string, wanted []string) (issue, force bool) {
	if strings.TrimSpace(installedCert) == "" {
		return true, false
	}
	if sanSetChanged(certSANs(sanOutput), wanted) {
		return true, true
	}
	return false, false
}

// reconcileMailCert ensures the issuance script + its systemd timer are in
// place, and runs the script immediately (forcing reissuance) whenever the
// names the installed certificate covers differ from the wanted SAN list —
// rather than waiting for the next
// scheduled tick — then restarts the mailserver only if the cert actually
// changed. See mailCertIssueScript for why this exists instead of copying a
// cert from Caddy.
func (f *fleet) reconcileMailCert(ctx context.Context, printer ui.Printer, client *sshx.Client) error {
	hostnames := mailCertHostnames(f.data.Deployment.DNS.RootDomain, f.data.Tenants)
	script := mailCertIssueScript(hostnames)

	// Reissue only when the names the installed certificate covers differ from the names wanted.
	// This used to compare the whole script text, so ANY edit of the script template (a fix to its
	// exit-status handling, say) counted as "the SAN list changed", forced a Let's Encrypt
	// reissuance and restarted the shared mail server on the next tenant apply.
	sanOut, _ := client.Run(ctx, "sudo openssl x509 -noout -ext subjectAltName -in "+mailTLSDir+"/fullchain.pem 2>/dev/null || true")

	if err := client.WriteFileSudo(ctx, mailCertScript, []byte(script), 0o700); err != nil {
		return fmt.Errorf("write %s: %w", mailCertScript, err)
	}
	if err := client.WriteFileSudo(ctx, mailCertService, []byte(mailCertServiceUnit), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", mailCertService, err)
	}
	if err := client.WriteFileSudo(ctx, mailCertTimer, []byte(mailCertTimerUnit), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", mailCertTimer, err)
	}
	if _, err := client.Run(ctx, "sudo systemctl daemon-reload && sudo systemctl enable --now mksrv-mail-cert.timer"); err != nil {
		return fmt.Errorf("enable mksrv-mail-cert.timer: %w", err)
	}

	oldCert, _ := client.Run(ctx, "sudo cat "+mailTLSDir+"/fullchain.pem 2>/dev/null || true")
	needIssue, forceIssue := mailCertDecision(oldCert.Stdout, sanOut.Stdout, hostnames)
	if needIssue {
		force := ""
		if forceIssue {
			force = " --force"
		}
		printer.Info("mail: issuing certificate for %s (this can take a minute)", strings.Join(hostnames, ", "))
		if _, err := client.Run(ctx, "sudo bash "+mailCertScript+force); err != nil {
			return fmt.Errorf("issue mail certificate: %w", err)
		}
	}

	newCert, _ := client.Run(ctx, "sudo cat "+mailTLSDir+"/fullchain.pem 2>/dev/null || true")
	if newCert.Stdout == oldCert.Stdout {
		return nil
	}
	if _, err := client.Run(ctx, "sudo systemctl restart mksrv-mailserver.service"); err != nil {
		return fmt.Errorf("restart mailserver: %w", err)
	}
	printer.Success("mail: certificate refreshed for %s", strings.Join(hostnames, ", "))
	return nil
}

// reconcileMailRelay pushes the operator SES SMTP credential (already
// mirrored to SSM by tenantSMTPSpec — requires mail.outbound_smtp: true) as
// the podman secrets mailserver.container.tmpl references when
// mail.relay_outbound is on. No-op when the flag is off. Does not itself
// restart mksrv-mailserver or add the RELAY_HOST env vars to its Quadlet
// unit — that's `mksrv deploy --stack mail` re-rendering the template, which
// must run after this so the secrets already exist when the container
// starts referencing them.
func (f *fleet) reconcileMailRelay(ctx context.Context, printer ui.Printer, client *sshx.Client) error {
	if f.data.Deployment.Mail == nil || !f.data.Deployment.Mail.RelayOutbound {
		return nil
	}
	user, err := f.resolver.Get(ctx, "/mksrv/{env}/mail/ses_smtp_user")
	if err != nil {
		return fmt.Errorf("mail relay: ses smtp user: %w (enable mail.outbound_smtp and run `mksrv apply` first)", err)
	}
	password, err := f.resolver.Get(ctx, "/mksrv/{env}/mail/ses_smtp_password")
	if err != nil {
		return fmt.Errorf("mail relay: ses smtp password: %w", err)
	}
	for name, val := range map[string]string{
		"mksrv-mail-relay-user":     user,
		"mksrv-mail-relay-password": password,
	} {
		if _, err := client.RunInput(ctx,
			"sudo podman secret create --replace "+quoteArg(name)+" -", []byte(val)); err != nil {
			return fmt.Errorf("mail relay: push %s: %w", name, err)
		}
	}
	if err := f.reconcileMailTenantRelay(ctx, client); err != nil {
		return err
	}
	printer.Success("mail: outbound relay credentials ready (run `mksrv deploy --stack mail` to apply)")
	return nil
}

// reconcileMailTenantRelay writes docker-mailserver's sender-dependent relay
// config (Phase C, draft ADR: per-tenant SES relay) — one line per hosted-mail
// tenant domain in each of postfix-relaymap.cf and postfix-sasl-password.cf,
// keyed by "@domain" (confirmed against the docker-mailserver docs: relay.sh
// turns the presence of these files into `sender_dependent_relayhost_maps` +
// `smtp_sasl_password_maps` + `smtp_sender_dependent_authentication = yes`).
// Every entry here points at the SAME relay host as the shared
// RELAY_HOST/RELAY_USER/RELAY_PASSWORD env vars reconcileMailRelay above
// already pushes — that combination (shared host, per-sender credential) is
// not shown verbatim in the docs, only its two halves separately, so this is
// additive on top of the existing default, never a replacement: a sender
// whose domain isn't in these two files still falls through to the env-var
// default exactly as before. Deliberately does not touch mailserver's Quadlet
// unit or restart it — same deploy-after-this contract as reconcileMailRelay.
func (f *fleet) reconcileMailTenantRelay(ctx context.Context, client *sshx.Client) error {
	relayMap, saslPasswd := mailTenantRelayFiles(f.data.Tenants, f.outputs.TenantMailSMTP, f.data.Deployment.AWS.Region)
	if err := client.WriteFileSudo(ctx, mailRelayMapPath, []byte(relayMap), 0o600); err != nil {
		return fmt.Errorf("mail relay: write %s: %w", mailRelayMapPath, err)
	}
	if err := client.WriteFileSudo(ctx, mailSASLPasswdPath, []byte(saslPasswd), 0o600); err != nil {
		return fmt.Errorf("mail relay: write %s: %w", mailSASLPasswdPath, err)
	}
	return nil
}

// mailTenantRelayFiles builds postfix-relaymap.cf and postfix-sasl-password.cf's
// content: one "@domain\t..." line per domain of every hosted-mail tenant that
// has a Phase B tenant SES identity (route53 dns_override). Sorted by domain
// for a deterministic diff between runs. A tenant with no entry in
// tenantSMTP (no route53 dns_override) is silently skipped — it has no
// per-tenant SES identity to relay through (falls through to the shared
// default relay instead, same as before this feature existed).
func mailTenantRelayFiles(tenants map[string]model.Tenant, tenantSMTP map[string]infra.TenantMailSMTPOutput, region string) (relayMap, saslPasswd string) {
	type entry struct{ domain, relayLine, saslLine string }
	var entries []entry
	relayHost := fmt.Sprintf("[email-smtp.%s.amazonaws.com]:587", region)
	ids := make([]string, 0, len(tenants))
	for id := range tenants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		tenant := tenants[id]
		if tenant.Mail == nil || !tenant.Mail.Hosted || len(tenant.Mail.Domains) == 0 {
			continue
		}
		out, ok := tenantSMTP[id]
		if !ok {
			continue
		}
		password := awsclient.DeriveSESSMTPPassword(out.SecretAccessKey, region)
		for _, d := range tenant.Mail.Domains {
			entries = append(entries, entry{
				domain:    d,
				relayLine: fmt.Sprintf("@%s\t%s", d, relayHost),
				saslLine:  fmt.Sprintf("@%s\t%s:%s", d, out.AccessKeyID, password),
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].domain < entries[j].domain })
	var relayBuf, saslBuf strings.Builder
	for _, e := range entries {
		relayBuf.WriteString(e.relayLine + "\n")
		saslBuf.WriteString(e.saslLine + "\n")
	}
	return relayBuf.String(), saslBuf.String()
}

// reconcileDKIM generates (once) and publishes the DKIM key for each of the
// tenant's mail domains. Idempotent: a key already on disk is only re-read,
// never regenerated, so rotation is a deliberate operator action, not a side
// effect of `tenant apply`.
func (f *fleet) reconcileDKIM(ctx context.Context, printer ui.Printer, client *sshx.Client, id string, t model.Tenant) error {
	zoneID := f.tenantZoneID(id, t)
	if t.DNSOverride == nil || t.DNSOverride.Provider != "route53" || zoneID == "" {
		return nil // caught by checkTenantMail; defensive no-op here
	}
	for _, domain := range t.Mail.Domains {
		// The key lands under the config bind mount (/tmp/docker-mailserver,
		// -> host /var/lib/mksrv/stacks/mail/config), not /var/mail-state:
		// ONE_DIR=1 consolidates the server's own *state* there, but DKIM
		// keys are operator-provided config, generated once by `setup` (see
		// below) and left under the same tree the operator's other config
		// files live in. Confirmed live against docker-mailserver 14.0.
		keyPath := fmt.Sprintf("/tmp/docker-mailserver/opendkim/keys/%s/mail.txt", domain)
		exists, _ := client.Run(ctx, fmt.Sprintf(
			"sudo podman exec mksrv-mailserver sh -c 'test -f %s && echo yes' || true", quoteArg(keyPath)))
		if strings.TrimSpace(exists.Stdout) != "yes" {
			// The CLI is `setup`, not `setup.sh` — docker-mailserver dropped
			// the .sh suffix at some point; confirmed live (`setup.sh` isn't
			// even on $PATH in 14.0, "executable file not found").
			if _, err := client.Run(ctx, "sudo podman exec mksrv-mailserver setup config dkim domain "+quoteArg(domain)); err != nil {
				return fmt.Errorf("mail %s: generate DKIM for %s: %w", id, domain, err)
			}
		}
		txt, err := client.Run(ctx, "sudo podman exec mksrv-mailserver cat "+quoteArg(keyPath))
		if err != nil {
			return fmt.Errorf("mail %s: read DKIM for %s: %w", id, domain, err)
		}
		value := parseDKIMRecord(txt.Stdout)
		if value == "" {
			return fmt.Errorf("mail %s: could not parse DKIM record for %s", id, domain)
		}
		if err := f.awsClients.UpsertTXT(ctx, zoneID, "mail._domainkey."+domain+".", value); err != nil {
			return fmt.Errorf("mail %s: publish DKIM for %s: %w", id, domain, err)
		}
		printer.Success("tenant %s: DKIM published for %s", id, domain)
	}
	return nil
}

// parseDKIMRecord extracts the `v=DKIM1; ...` value out of docker-mailserver's
// mail.txt, which wraps it in BIND zone-file syntax across possibly several
// quoted strings on one or more lines, e.g.:
//
//	mail._domainkey IN TXT ( "v=DKIM1; h=sha256; k=rsa; "
//	    "p=MIIBIjANBg...IDAQAB" )
func parseDKIMRecord(raw string) string {
	re := regexp.MustCompile(`"([^"]*)"`)
	matches := re.FindAllStringSubmatch(raw, -1)
	var b strings.Builder
	for _, m := range matches {
		b.WriteString(m[1])
	}
	value := b.String()
	if !strings.Contains(value, "v=DKIM1") {
		return ""
	}
	return value
}

// mtaSTSFragmentPath is the Caddy fragment mksrv writes for one tenant domain's
// MTA-STS policy host. Prefix 16- (unused — 15 is mail.caddy, 20 is
// database.caddy): grouped with mail-adjacent fragments, ahead of tenant web
// vhosts.
func mtaSTSFragmentPath(id, domain string) string {
	return fmt.Sprintf("/var/lib/mksrv/caddy.d/16-mta-sts-%s-%s.caddy", id, domain)
}

// mtaSTSPolicy renders the RFC 8461 policy body served at
// /.well-known/mta-sts.txt. Always `mode: testing` — see the MTASTS field doc
// (internal/model/model.go): mksrv has no way to know a prior policy already
// existed in enforce, or to monitor delivery before flipping to it safely.
func mtaSTSPolicy(mxHostname string) string {
	return fmt.Sprintf("version: STSv1\nmode: testing\nmx: %s\nmax_age: 604800\n", mxHostname)
}

// mtaSTSFragment is the Caddy site block for one domain: only the
// well-known path is answered; anything else 404s rather than falling
// through to Caddy's own default (which would otherwise expose whatever the
// edge's catch-all does under a hostname that's supposed to be MTA-STS-only).
func mtaSTSFragment(domain, mxHostname string) string {
	policy := mtaSTSPolicy(mxHostname)
	return fmt.Sprintf(`mta-sts.%s {
	handle /.well-known/mta-sts.txt {
		header Content-Type "text/plain"
		respond %q 200
	}
	handle {
		respond 404
	}
}
`, domain, policy)
}

// provisionMTASTS reconciles the mta-sts.<domain> Caddy fragment on the edge
// for every selected tenant with `mail.mta_sts: true` (ADR 0032). The
// matching mta-sts.<domain> -> edge DNS record comes from Terraform's
// dns_tenant module (`mksrv apply --infra-only`), same ordering dependency as
// branded_hostname: the record has to exist before Caddy can get a cert for
// the hostname. Fragments for dropped domains/tenants are removed.
func (f *fleet) provisionMTASTS(ctx context.Context, printer ui.Printer, edgeClient *sshx.Client, tenants []string) error {
	mxHostname := "mail." + f.data.Deployment.DNS.RootDomain
	changed := 0
	for _, id := range tenants {
		t, ok := f.data.Tenants[id]
		if !ok {
			continue
		}
		want := map[string]string{}
		if tenantMailHosted(t) && t.Mail.MTASTS {
			for _, d := range t.Mail.Domains {
				want[mtaSTSFragmentPath(id, d)] = mtaSTSFragment(d, mxHostname)
			}
		}
		res, err := edgeClient.Run(ctx, fmt.Sprintf("ls /var/lib/mksrv/caddy.d/16-mta-sts-%s-*.caddy 2>/dev/null || true", quoteArg(id)))
		if err == nil {
			for _, line := range strings.Fields(res.Stdout) {
				if _, keep := want[line]; !keep {
					if _, err := edgeClient.Run(ctx, "sudo rm -f "+quoteArg(line)); err != nil {
						return fmt.Errorf("tenant %s: remove stale mta-sts fragment %s: %w", id, line, err)
					}
					changed++
				}
			}
		}
		for _, path := range sortedKeys(want) {
			old, _ := edgeClient.Run(ctx, "sudo cat "+quoteArg(path)+" 2>/dev/null || true")
			if old.Stdout == want[path] {
				continue
			}
			if err := edgeClient.WriteFileSudo(ctx, path, []byte(want[path]), 0o644); err != nil {
				return fmt.Errorf("tenant %s: write mta-sts fragment %s: %w", id, path, err)
			}
			changed++
		}
		if len(want) > 0 {
			printer.Success("tenant %s: mta-sts policy (testing mode) for %d domain(s)", id, len(want))
		}
	}
	if changed == 0 {
		return nil
	}
	if _, err := edgeClient.Run(ctx,
		"sudo podman exec mksrv-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile || sudo systemctl restart mksrv-caddy.service"); err != nil {
		return fmt.Errorf("reload edge caddy for mta-sts: %w", err)
	}
	return nil
}

// tenantZoneID is the Route 53 zone mksrv writes the tenant's records into: the
// declared zone_id, or the zone it created (dns_override.create_zone, ADR 0043)
// as recorded in the infra outputs. Empty when the tenant has neither.
func (f *fleet) tenantZoneID(id string, t model.Tenant) string {
	if t.DNSOverride == nil {
		return ""
	}
	if z := strings.TrimSpace(t.DNSOverride.ZoneID); z != "" {
		return z
	}
	if t.DNSOverride.CreateZone {
		return f.outputs.TenantZones[id].ZoneID
	}
	return ""
}
