// SPDX-License-Identifier: Apache-2.0

// Package configd is the broker that issues signed Cloud-IT VPN client
// configurations. It validates a Keycloak OIDC access token, resolves the
// tenant, mints a one-use Headscale pre-auth key, and returns a compact
// Ed25519 JWS whose payload is the clientconfig the desktop app verifies.
package configd

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
)

// ClientConfig mirrors the structure Cloud-IT VPN's verifier accepts. Every
// field is emitted because the verifier rejects unknown and, in practice,
// expects the full shape.
type ClientConfig struct {
	Version   int       `json:"version"`
	IssuedAt  int64     `json:"iat"`
	Tenant    Tenant    `json:"tenant"`
	Tailnet   Tailnet   `json:"tailnet"`
	Forwards  []Forward `json:"forwards"`
	Policy    Policy    `json:"policy"`
	Update    Update    `json:"update"`
	Telemetry Telemetry `json:"telemetry"`
}

type Tenant struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Branding    Branding `json:"branding"`
}

type Branding struct {
	Primary     string `json:"primary"`
	LogoDataURI string `json:"logoDataUri"`
}

type Tailnet struct {
	ControlURL string `json:"controlUrl"`
	PreauthKey string `json:"preauthKey"`
	NodeName   string `json:"nodeName"`
	Ephemeral  bool   `json:"ephemeral"`
}

type Forward struct {
	ID           string      `json:"id"`
	Label        string      `json:"label"`
	Type         string      `json:"type"`
	Listen       Listen      `json:"listen"`
	PortStrategy string      `json:"portStrategy"`
	Target       string      `json:"target"`
	OpenAction   OpenAction  `json:"openAction"`
	HealthCheck  HealthCheck `json:"healthCheck"`
	MaxConns     int         `json:"maxConns"`
	SSH          *SSH        `json:"ssh,omitempty"`
}

type Listen struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type OpenAction struct {
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Template string `json:"template,omitempty"`
}

type HealthCheck struct {
	Kind        string `json:"kind"`
	Path        string `json:"path,omitempty"`
	IntervalSec int    `json:"intervalSec"`
}

type SSH struct {
	HostKeyAlias   string `json:"hostKeyAlias"`
	WriteConfig    bool   `json:"writeSshConfig"`
	ConfigAlias    string `json:"sshConfigAlias"`
	KnownHostsFile string `json:"knownHostsFile"`
}

type Policy struct {
	Reconnect         Reconnect `json:"reconnect"`
	IdleDisconnectMin int       `json:"idleDisconnectMin"`
	KillSwitch        bool      `json:"killSwitch"`
}

type Reconnect struct {
	BackoffSec []int `json:"backoffSec"`
}

type Update struct {
	FeedURL    string `json:"feedUrl"`
	MinVersion string `json:"minVersion"`
}

type Telemetry struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
}

// Signer produces compact Ed25519 JWS values.
type Signer struct {
	kid string
	key ed25519.PrivateKey
}

// NewSigner builds a signer from a raw Ed25519 private key (64 bytes) or seed
// (32 bytes) and a key id.
func NewSigner(kid string, keyBytes []byte) (*Signer, error) {
	switch len(keyBytes) {
	case ed25519.PrivateKeySize:
		return &Signer{kid: kid, key: ed25519.PrivateKey(keyBytes)}, nil
	case ed25519.SeedSize:
		return &Signer{kid: kid, key: ed25519.NewKeyFromSeed(keyBytes)}, nil
	default:
		return nil, fmt.Errorf("ed25519 key must be %d or %d bytes, got %d", ed25519.SeedSize, ed25519.PrivateKeySize, len(keyBytes))
	}
}

// PublicKey returns the signer's public key.
func (s *Signer) PublicKey() ed25519.PublicKey {
	return s.key.Public().(ed25519.PublicKey)
}

// ParsePrivateKey accepts a PKCS#8 PEM Ed25519 private key, a base64-encoded
// seed (32 bytes) or key (64 bytes), or raw key bytes, and returns key bytes
// NewSigner accepts.
func ParsePrivateKey(raw []byte) ([]byte, error) {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, "-----BEGIN") {
		block, _ := pem.Decode([]byte(text))
		if block == nil {
			return nil, fmt.Errorf("invalid PEM block")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#8 key: %w", err)
		}
		key, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PEM key is not Ed25519")
		}
		return key, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(text); err == nil && (len(decoded) == ed25519.SeedSize || len(decoded) == ed25519.PrivateKeySize) {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(text); err == nil && (len(decoded) == ed25519.SeedSize || len(decoded) == ed25519.PrivateKeySize) {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(text); err == nil && (len(decoded) == ed25519.SeedSize || len(decoded) == ed25519.PrivateKeySize) {
		return decoded, nil
	}
	if len(raw) == ed25519.SeedSize || len(raw) == ed25519.PrivateKeySize {
		return raw, nil
	}
	return nil, fmt.Errorf("unrecognised Ed25519 private key encoding")
}

// Sign encodes cfg as the JWS payload and returns the compact serialization.
func (s *Signer) Sign(cfg ClientConfig) (string, error) {
	header := map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": s.kid}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	signingInput := b64(headerJSON) + "." + b64(payloadJSON)
	signature := ed25519.Sign(s.key, []byte(signingInput))
	return signingInput + "." + b64(signature), nil
}

func b64(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// PublicKeyPEM returns the PKIX/PEM encoding of an Ed25519 public key, the form
// Cloud-IT VPN's tenant.json expects in signingKeys[].publicKeyPem.
func PublicKeyPEM(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}
