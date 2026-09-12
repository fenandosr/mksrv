// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"
	"strings"

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

// txtSegmentMax is the longest a single DNS TXT character-string may be
// (RFC 1035: length-prefixed by one octet, so 255 is the hard ceiling — not
// an AWS-specific limit). A DKIM RSA public key comfortably exceeds this.
const txtSegmentMax = 255

// quoteTXT renders a TXT record value the way Route53 requires: one or more
// quoted character-strings, none longer than txtSegmentMax, concatenated
// with a space. A short value (most TXT records) round-trips as a single
// quoted string, same as before; a long one (a DKIM public key) gets split
// into 255-byte chunks — Route53 rejects a single over-long quoted string
// with "CharacterStringTooLong (Value is too long)". Splits on bytes, not
// runes: DKIM/base64 content is pure ASCII, so this never divides a
// multi-byte rune, but byte length is what the 255-octet limit is actually
// counting.
func quoteTXT(value string) string {
	if value == "" {
		return `""`
	}
	b := []byte(value)
	var segments []string
	for len(b) > 0 {
		n := min(len(b), txtSegmentMax)
		segments = append(segments, `"`+string(b[:n])+`"`)
		b = b[n:]
	}
	return strings.Join(segments, " ")
}
