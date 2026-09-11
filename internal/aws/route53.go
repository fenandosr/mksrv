// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

// UpsertTXT writes one TXT record into zoneID, replacing any prior value under
// that name. value is the literal TXT content (mksrv adds the surrounding
// quotes Route53 requires). The only caller today is the mail stack's DKIM
// publisher (ADR 0032) — every other mksrv-managed DNS record goes through
// Terraform (`dns:`, `web:`, MX/SPF/DMARC), which can compute its value
// without touching live infrastructure. DKIM can't: the keypair only exists
// after `docker-mailserver` generates it on the running container.
func (c *Clients) UpsertTXT(ctx context.Context, zoneID, fqdn, value string) error {
	if zoneID == "" {
		return fmt.Errorf("upsert TXT %s: empty zone id", fqdn)
	}
	comment := "mksrv: DKIM (ADR 0032)"
	_, err := c.route53.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: awssdk.String(zoneID),
		ChangeBatch: &types.ChangeBatch{
			Comment: awssdk.String(comment),
			Changes: []types.Change{{
				Action: types.ChangeActionUpsert,
				ResourceRecordSet: &types.ResourceRecordSet{
					Name: awssdk.String(fqdn),
					Type: types.RRTypeTxt,
					TTL:  awssdk.Int64(300),
					ResourceRecords: []types.ResourceRecord{
						{Value: awssdk.String(quoteTXT(value))},
					},
				},
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("upsert TXT %s in zone %s: %w", fqdn, zoneID, err)
	}
	return nil
}

// quoteTXT wraps a TXT record value in the quotes Route53 requires, unless the
// caller already supplied them (docker-mailserver's DKIM output is often
// pre-quoted).
func quoteTXT(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return value
	}
	return `"` + value + `"`
}
