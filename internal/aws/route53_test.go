// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"regexp"
	"strings"
	"testing"
)

// TestQuoteTXTShort guards the common case (SPF/DMARC, short values): a
// single quoted string, unchanged behavior from before chunking existed.
func TestQuoteTXTShort(t *testing.T) {
	t.Parallel()
	if got := quoteTXT("v=spf1 mx ~all"); got != `"v=spf1 mx ~all"` {
		t.Fatalf("quoteTXT() = %q", got)
	}
	if got := quoteTXT(""); got != `""` {
		t.Fatalf("quoteTXT(\"\") = %q, want an empty quoted string", got)
	}
}

// TestQuoteTXTChunksLongValues guards a real publish failure: Route53
// rejected a DKIM public key wrapped in one long quoted string with
// "CharacterStringTooLong (Value is too long)" — RFC 1035 caps a single TXT
// character-string at 255 bytes, DKIM keys routinely exceed that.
func TestQuoteTXTChunksLongValues(t *testing.T) {
	t.Parallel()
	// The exact DKIM value that failed live (v=DKIM1 tag + a 2048-bit RSA
	// public key, base64) — 384 bytes, comfortably over one 255-byte segment.
	value := `v=DKIM1; h=sha256; k=rsa; p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAuXRFFlLz3/+JTSMzVkPK4v+EwlJKoLnhzsw0p0aujmAzh0adfS3XvDPSvxR4y78xgl52VIE7ms77DbbrCVK0teF5pRsSt8dY2/2faOuk1gOt620js9uDwVHhxtdm/INN1wp5XJD3GqFpk32OvS3rLjs5LVcakpG+o9XUhOaFnnSJVlZeDZ7yjjyKJZPLbPsYV5kN6dE8QqmbVHWVsJ/iSRshJbkdzTWftQfJcx81V3HdaTD7bq/0uNpYWWX8ayJN+qz2Pik7FKBylSyznO5Bc7rd2PITxsThZWC23BA2X7mmtk7323L9vm0s3SbeHAOAmb/CfhzzqmL6mi24Byv5bwIDAQAB`
	if len(value) <= txtSegmentMax {
		t.Fatalf("test fixture is only %d bytes, must exceed txtSegmentMax (%d) to exercise chunking", len(value), txtSegmentMax)
	}

	got := quoteTXT(value)

	// Segments are adjacent quoted strings separated by a single space
	// (`"a" "b"`); the content itself may contain spaces (this DKIM value's
	// "v=DKIM1; h=sha256; ..." prefix does), so split on the quote
	// boundaries, not on every space.
	quotedSegment := regexp.MustCompile(`"[^"]*"`)
	segments := quotedSegment.FindAllString(got, -1)
	if joined := strings.Join(segments, " "); joined != got {
		t.Fatalf("quoteTXT() has content outside quoted segments:\ngot:    %q\nparsed: %q", got, joined)
	}
	var rebuilt strings.Builder
	for i, seg := range segments {
		inner := seg[1 : len(seg)-1]
		if len(inner) > txtSegmentMax {
			t.Fatalf("segment %d is %d bytes, over the %d-byte TXT character-string limit", i, len(inner), txtSegmentMax)
		}
		rebuilt.WriteString(inner)
	}
	if rebuilt.String() != value {
		t.Fatalf("chunked value doesn't reassemble to the original:\ngot:  %s\nwant: %s", rebuilt.String(), value)
	}
	if len(segments) < 2 {
		t.Fatalf("expected at least 2 segments for a %d-byte value, got %d", len(value), len(segments))
	}
}
