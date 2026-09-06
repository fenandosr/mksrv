// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// TestDeriveSESSMTPPassword pins AWS's published derivation. The algorithm was
// confirmed end to end on 2026-09-06 — a key derived by this function
// authenticated over STARTTLS + AUTH LOGIN against
// email-smtp.us-east-1.amazonaws.com:587 and SES accepted the message — so the
// vectors below are a regression lock, not just a structural check.
func TestDeriveSESSMTPPassword(t *testing.T) {
	t.Parallel()

	// A fixed dummy secret (AWS's own docs example string, not a real key).
	const dummySecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

	cases := map[string]string{
		"us-east-1": "BLBM/9hSUELfq8Gw+rU1YcBjkOxGbhT2XG763xVLGWL9",
		"eu-west-1": "BMW5RDrXmmVs0lV7GpI4oLkHXpZ4stDsk6q91z1g38Pk",
	}
	for region, want := range cases {
		if got := DeriveSESSMTPPassword(dummySecret, region); got != want {
			t.Errorf("DeriveSESSMTPPassword(_, %q) = %q, want %q", region, got, want)
		}
	}

	a := DeriveSESSMTPPassword(dummySecret, "us-east-1")
	if b := DeriveSESSMTPPassword(dummySecret, "us-east-1"); a != b {
		t.Fatalf("not deterministic: %q != %q", a, b)
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
