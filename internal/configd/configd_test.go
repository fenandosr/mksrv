// SPDX-License-Identifier: Apache-2.0

package configd

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testConfig() ClientConfig {
	return ClientConfig{
		Version:  1,
		IssuedAt: time.Now().Unix(),
		Tenant:   Tenant{ID: "bitabit", DisplayName: "Bit-a-Bit", Branding: Branding{Primary: "#0C6D77"}},
		Tailnet:  Tailnet{ControlURL: "https://vpn.example.com", PreauthKey: "hskey-abc", NodeName: "bitabit-laptop"},
		Forwards: []Forward{{
			ID: "database", Label: "PostgreSQL", Type: "tcp",
			Listen: Listen{Host: "127.0.0.1"}, PortStrategy: "auto",
			Target: "prod-data.prod.mksrv:5432", OpenAction: OpenAction{Kind: "none"},
			HealthCheck: HealthCheck{Kind: "tcp", IntervalSec: 30}, MaxConns: 16,
		}},
		Policy:    Policy{Reconnect: Reconnect{BackoffSec: []int{1, 2, 5}}},
		Update:    Update{FeedURL: "https://dl.example.com/appcast.json", MinVersion: "0.1.0"},
		Telemetry: Telemetry{},
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	t.Parallel()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	signer, err := NewSigner("mksrv-prod-1", seed)
	if err != nil {
		t.Fatalf("NewSigner() error = %v", err)
	}
	compact, err := signer.Sign(testConfig())
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		t.Fatalf("compact JWS has %d parts", len(parts))
	}

	var header struct{ Alg, Typ, Kid string }
	headerRaw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		t.Fatal(err)
	}
	if header.Alg != "EdDSA" || header.Typ != "JWT" || header.Kid != "mksrv-prod-1" {
		t.Fatalf("header = %+v", header)
	}

	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(signer.PublicKey(), []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("signature does not verify with the signer public key")
	}

	payloadRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var cfg ClientConfig
	if err := json.Unmarshal(payloadRaw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Tenant.ID != "bitabit" || cfg.Tailnet.PreauthKey != "hskey-abc" {
		t.Fatalf("payload = %+v", cfg)
	}
}

func TestParsePrivateKeyForms(t *testing.T) {
	t.Parallel()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = 7
	}
	// base64url (the form EnsureRandom produces)
	got, err := ParsePrivateKey([]byte(base64.RawURLEncoding.EncodeToString(seed)))
	if err != nil || len(got) != ed25519.SeedSize {
		t.Fatalf("base64url seed: got %d bytes, err=%v", len(got), err)
	}
}

func TestPublicKeyPEM(t *testing.T) {
	t.Parallel()
	pub, _, _ := ed25519.GenerateKey(nil)
	pemStr, err := PublicKeyPEM(pub)
	if err != nil || !strings.Contains(pemStr, "BEGIN PUBLIC KEY") {
		t.Fatalf("PublicKeyPEM() = %q, err=%v", pemStr, err)
	}
}

func TestClaimsVPNEntitled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		groups []string
		want   bool
	}{
		{nil, false},
		{[]string{"apps"}, false},
		{[]string{"apps", "vpn"}, true},
		{[]string{"dev"}, true},
		{[]string{"admin"}, true},
		{[]string{"other"}, false},
	}
	for _, tc := range cases {
		if got := (Claims{Groups: tc.groups}).VPNEntitled(); got != tc.want {
			t.Errorf("VPNEntitled(%v) = %v, want %v", tc.groups, got, tc.want)
		}
	}
}

func TestClaimsUnmarshalGroups(t *testing.T) {
	t.Parallel()
	var c Claims
	if err := json.Unmarshal([]byte(`{"iss":"x","groups":["dev","vpn"]}`), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Groups) != 2 || c.Groups[0] != "dev" {
		t.Fatalf("groups = %v", c.Groups)
	}
}
