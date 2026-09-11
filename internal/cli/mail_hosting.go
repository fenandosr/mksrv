// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/fenandosr/mksrv/internal/model"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

const (
	mailConfigPath = "/var/lib/mksrv/stacks/mail/config/postfix-accounts.cf"
	mailTLSDir     = "/var/lib/mksrv/stacks/mail/tls"
)

var mailLocalPartRe = regexp.MustCompile(`[^a-z0-9]+`)

// mailPasswordRef is the SSM path for one mailbox's generated password.
func mailPasswordRef(tenantID, address string) string {
	local := mailLocalPartRe.ReplaceAllString(strings.ToLower(model.TenantMailbox{Address: address}.MailLocalPart()), "_")
	return "/mksrv/{env}/mail/tenant_" + tenantID + "_" + local + "_password"
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

// mailTenants returns every tenant declaring a `mail:` block with at least one
// domain, sorted by id — the full set, not just this run's `--tenant`
// selection, so a partial `tenant apply <id>` never drops another tenant's
// mailboxes from the shared accounts file.
func mailTenants(tenants map[string]model.Tenant) []string {
	var ids []string
	for id, t := range tenants {
		if t.Mail != nil && len(t.Mail.Domains) > 0 {
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

	if err := f.reconcileMailboxes(ctx, printer, client, all); err != nil {
		return err
	}
	if err := f.reconcileMailTLS(ctx, printer, client); err != nil {
		return err
	}
	for _, id := range selected {
		t, ok := f.data.Tenants[id]
		if !ok || t.Mail == nil {
			continue
		}
		if err := f.reconcileDKIM(ctx, printer, client, id, t); err != nil {
			return err
		}
	}
	return nil
}

// reconcileMailboxes writes postfix-accounts.cf for every mailbox across every
// mail-consuming tenant and restarts the server only if it changed.
func (f *fleet) reconcileMailboxes(ctx context.Context, printer ui.Printer, client *sshx.Client, tenantIDs []string) error {
	type line struct {
		address, hash string
	}
	var lines []line
	for _, id := range tenantIDs {
		t := f.data.Tenants[id]
		for _, mb := range t.Mail.Mailboxes {
			pass, err := f.resolver.EnsureRandom(ctx, mailPasswordRef(id, mb.Address), 24)
			if err != nil {
				return fmt.Errorf("mail %s: password for %s: %w", id, mb.Address, err)
			}
			res, err := client.RunInput(ctx, "openssl passwd -6 -stdin", []byte(pass))
			if err != nil {
				return fmt.Errorf("mail %s: hash password for %s: %w", id, mb.Address, err)
			}
			lines = append(lines, line{strings.ToLower(mb.Address), strings.TrimSpace(res.Stdout)})
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
		return nil
	}
	if err := client.WriteFileSudo(ctx, mailConfigPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", mailConfigPath, err)
	}
	if _, err := client.Run(ctx, "sudo systemctl restart mksrv-mailserver.service"); err != nil {
		return fmt.Errorf("restart mailserver: %w", err)
	}
	printer.Success("mail: %d mailbox(es) across %d tenant(s)", len(lines), len(tenantIDs))
	return nil
}

// reconcileMailTLS copies the cert Caddy holds for the shared mail hostname
// into the mailserver's manual-TLS mount, restarting only on change.
func (f *fleet) reconcileMailTLS(ctx context.Context, printer ui.Printer, client *sshx.Client) error {
	hostname := "mail." + f.data.Deployment.DNS.RootDomain
	find := fmt.Sprintf(
		"sudo podman exec mksrv-caddy sh -c 'find /data/caddy/certificates -type d -iname %s 2>/dev/null | head -1'",
		quoteArg(hostname),
	)
	res, err := client.Run(ctx, find)
	if err != nil || strings.TrimSpace(res.Stdout) == "" {
		printer.Warn("mail: no cert for %s in Caddy yet (it appears after the first `tenant apply` + a Caddy reload); skipping TLS copy", hostname)
		return nil
	}
	dir := strings.TrimSpace(res.Stdout)

	cert, err := client.Run(ctx, "sudo podman exec mksrv-caddy cat "+quoteArg(dir+"/"+hostname+".crt"))
	if err != nil {
		return fmt.Errorf("read mail cert: %w", err)
	}
	key, err := client.Run(ctx, "sudo podman exec mksrv-caddy cat "+quoteArg(dir+"/"+hostname+".key"))
	if err != nil {
		return fmt.Errorf("read mail key: %w", err)
	}

	oldCert, _ := client.Run(ctx, "sudo cat "+mailTLSDir+"/fullchain.pem 2>/dev/null || true")
	if oldCert.Stdout == cert.Stdout {
		return nil
	}
	if _, err := client.Run(ctx, "sudo mkdir -p "+mailTLSDir); err != nil {
		return err
	}
	if err := client.WriteFileSudo(ctx, mailTLSDir+"/fullchain.pem", []byte(cert.Stdout), 0o644); err != nil {
		return err
	}
	if err := client.WriteFileSudo(ctx, mailTLSDir+"/privkey.pem", []byte(key.Stdout), 0o600); err != nil {
		return err
	}
	if _, err := client.Run(ctx, "sudo systemctl restart mksrv-mailserver.service"); err != nil {
		return fmt.Errorf("restart mailserver: %w", err)
	}
	printer.Success("mail: TLS cert for %s refreshed", hostname)
	return nil
}

// reconcileDKIM generates (once) and publishes the DKIM key for each of the
// tenant's mail domains. Idempotent: a key already on disk is only re-read,
// never regenerated, so rotation is a deliberate operator action, not a side
// effect of `tenant apply`.
func (f *fleet) reconcileDKIM(ctx context.Context, printer ui.Printer, client *sshx.Client, id string, t model.Tenant) error {
	if t.DNSOverride == nil || t.DNSOverride.Provider != "route53" || t.DNSOverride.ZoneID == "" {
		return nil // caught by checkTenantMail; defensive no-op here
	}
	for _, domain := range t.Mail.Domains {
		keyPath := fmt.Sprintf("/var/mail-state/opendkim/keys/%s/mail.txt", domain)
		exists, _ := client.Run(ctx, fmt.Sprintf(
			"sudo podman exec mksrv-mailserver sh -c 'test -f %s && echo yes' || true", quoteArg(keyPath)))
		if strings.TrimSpace(exists.Stdout) != "yes" {
			if _, err := client.Run(ctx, "sudo podman exec mksrv-mailserver setup.sh config dkim domain "+quoteArg(domain)); err != nil {
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
		if err := f.awsClients.UpsertTXT(ctx, t.DNSOverride.ZoneID, "mail._domainkey."+domain+".", value); err != nil {
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
