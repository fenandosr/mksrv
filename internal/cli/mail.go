// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"

	awsclient "github.com/fenandosr/mksrv/internal/aws"
	"github.com/fenandosr/mksrv/internal/keycloak"
	"github.com/fenandosr/mksrv/internal/model"
)

// tenantSMTPSpec builds the shared SMTP config every tenant realm gets
// (ADR 0025) from Terraform's `mail_smtp` output, mirroring the derived SMTP
// credential into SSM so later reads don't need Terraform state. Returns nil
// (no realm SMTP change) when `mail.outbound_smtp` is off.
func (f *fleet) tenantSMTPSpec(ctx context.Context) (*keycloak.SMTPSpec, error) {
	out := f.outputs.MailSMTP
	if !out.Enabled {
		return nil, nil
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return nil, err
	}
	password := awsclient.DeriveSESSMTPPassword(out.SecretAccessKey, f.data.Deployment.AWS.Region)
	if err := f.resolver.Put(ctx, "/mksrv/{env}/mail/ses_smtp_user", out.AccessKeyID); err != nil {
		return nil, fmt.Errorf("mirror ses smtp user: %w", err)
	}
	if err := f.resolver.Put(ctx, "/mksrv/{env}/mail/ses_smtp_password", password); err != nil {
		return nil, fmt.Errorf("mirror ses smtp password: %w", err)
	}
	return &keycloak.SMTPSpec{
		Host:        out.SMTPHost,
		Port:        out.SMTPPort,
		From:        out.FromAddress,
		FromDisplay: "Cloud-IT",
		User:        out.AccessKeyID,
		Password:    password,
	}, nil
}

// tenantSMTPSpecFor builds a tenant's OWN SMTP config (Phase C, draft ADR:
// per-tenant SES relay) from Terraform's `tenant_mail_smtp[id]` output —
// present for every tenant with a route53 dns_override (Phase B), regardless
// of mail.outbound_smtp/mail.hosted. Falls back to the shared operator
// credential (tenantSMTPSpec) when this tenant has no entry — a tenant
// without its own verified domain identity still gets Keycloak transactional
// email the way every tenant did before this, rather than none at all.
func (f *fleet) tenantSMTPSpecFor(ctx context.Context, id string, tenant model.Tenant) (*keycloak.SMTPSpec, error) {
	out, ok := f.outputs.TenantMailSMTP[id]
	if !ok {
		return f.tenantSMTPSpec(ctx)
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return nil, err
	}
	password := awsclient.DeriveSESSMTPPassword(out.SecretAccessKey, f.data.Deployment.AWS.Region)
	userPath := "/mksrv/{env}/mail/tenant_" + id + "_ses_smtp_user"
	passPath := "/mksrv/{env}/mail/tenant_" + id + "_ses_smtp_password"
	if err := f.resolver.Put(ctx, userPath, out.AccessKeyID); err != nil {
		return nil, fmt.Errorf("mirror tenant %s ses smtp user: %w", id, err)
	}
	if err := f.resolver.Put(ctx, passPath, password); err != nil {
		return nil, fmt.Errorf("mirror tenant %s ses smtp password: %w", id, err)
	}
	return &keycloak.SMTPSpec{
		Host:        out.SMTPHost,
		Port:        out.SMTPPort,
		From:        out.FromAddress,
		FromDisplay: tenant.DisplayName,
		User:        out.AccessKeyID,
		Password:    password,
	}, nil
}
