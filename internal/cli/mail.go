// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"

	awsclient "github.com/fenandosr/mksrv/internal/aws"
	"github.com/fenandosr/mksrv/internal/keycloak"
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
