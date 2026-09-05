// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// TestDeriveSESSMTPPassword checks the structural properties of AWS's
// published derivation (deterministic, correct length, version-byte-prefixed
// base64) — this session had no live SES SMTP endpoint to confirm the exact
// bytes against, so treat this as "the algorithm is wired up as documented",
// and spot-check once against a real IAM key by actually authenticating over
// SMTP before relying on it in production.
func TestDeriveSESSMTPPassword(t *testing.T) {
	t.Parallel()
	a := DeriveSESSMTPPassword("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1")
	b := DeriveSESSMTPPassword("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1")
	if a != b {
		t.Fatalf("not deterministic: %q != %q", a, b)
	}
	if c := DeriveSESSMTPPassword("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "eu-west-1"); c == a {
		t.Fatalf("region must change the derived password")
	}

	decoded, err := base64.StdEncoding.DecodeString(a)
	if err != nil {
		t.Fatalf("not valid base64: %v", err)
	}
	if len(decoded) != 1+sha256.Size {
		t.Fatalf("decoded length = %d, want %d (1 version byte + sha256)", len(decoded), 1+sha256.Size)
	}
	if decoded[0] != sesSMTPVersion {
		t.Fatalf("version byte = %#x, want %#x", decoded[0], sesSMTPVersion)
	}
}
