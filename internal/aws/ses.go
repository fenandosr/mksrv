// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// SES SMTP passwords are derived from an IAM secret access key by AWS's
// published SigV4-based conversion (Signing Version 4), not sent as-is: SMTP
// AUTH has no notion of an access-key/secret-key pair, only a single
// username/password, so this is the one-way function that produces the
// password half from the IAM secret. The inputs below (the literal string
// "11111111" as a stand-in date, the fixed "aws4_request" terminal, and the
// version byte 0x04) are exactly as AWS's own reference implementation
// defines them — not values mksrv chose.
const (
	sesSMTPDate     = "11111111"
	sesSMTPService  = "ses"
	sesSMTPTerminal = "aws4_request"
	sesSMTPMessage  = "SendRawEmail"
	sesSMTPVersion  = 0x04
)

func sesHMAC(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// DeriveSESSMTPPassword converts an IAM secret access key into the SMTP
// password Keycloak's realm SMTP settings (and any other plain-SMTP client)
// authenticate with. The IAM access key id is used unchanged as the SMTP
// username.
func DeriveSESSMTPPassword(secretAccessKey, region string) string {
	k := sesHMAC([]byte("AWS4"+secretAccessKey), []byte(sesSMTPDate))
	k = sesHMAC(k, []byte(region))
	k = sesHMAC(k, []byte(sesSMTPService))
	k = sesHMAC(k, []byte(sesSMTPTerminal))
	k = sesHMAC(k, []byte(sesSMTPMessage))
	versioned := append([]byte{sesSMTPVersion}, k...)
	return base64.StdEncoding.EncodeToString(versioned)
}
