// SPDX-License-Identifier: Apache-2.0

package configd

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Claims is the subset of an access token configd inspects.
type Claims struct {
	Issuer   string   `json:"iss"`
	Subject  string   `json:"sub"`
	Expiry   int64    `json:"exp"`
	IssuedAt int64    `json:"iat"`
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	AZP      string   `json:"azp"`
	Audience audience `json:"aud"`
}

type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*a = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// Verifier validates RS256 Keycloak access tokens against a realm's JWKS.
type Verifier struct {
	http *http.Client
	mu   sync.Mutex
	keys map[string]cachedKeys // issuer -> keys
}

type cachedKeys struct {
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// NewVerifier creates a JWKS-backed token verifier.
func NewVerifier() *Verifier {
	return &Verifier{
		http: &http.Client{Timeout: 10 * time.Second},
		keys: map[string]cachedKeys{},
	}
}

// Verify checks the token signature and expiry and returns its claims. Only
// issuers in allowed are accepted.
func (v *Verifier) Verify(ctx context.Context, token string, allowed map[string]bool, now time.Time) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("token is not a JWT")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode token header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return nil, err
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported token alg %q", header.Alg)
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode token claims: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		return nil, err
	}
	if !allowed[claims.Issuer] {
		return nil, fmt.Errorf("token issuer %q is not a known tenant realm", claims.Issuer)
	}
	if now.Unix() >= claims.Expiry {
		return nil, fmt.Errorf("token is expired")
	}

	key, err := v.keyFor(ctx, claims.Issuer, header.Kid)
	if err != nil {
		return nil, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode token signature: %w", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return nil, fmt.Errorf("token signature invalid: %w", err)
	}
	return &claims, nil
}

func (v *Verifier) keyFor(ctx context.Context, issuer, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	entry, ok := v.keys[issuer]
	v.mu.Unlock()
	if ok && time.Since(entry.fetched) < time.Hour {
		if key, ok := entry.keys[kid]; ok {
			return key, nil
		}
	}
	keys, err := v.fetchJWKS(ctx, issuer)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.keys[issuer] = cachedKeys{keys: keys, fetched: time.Now()}
	v.mu.Unlock()
	key, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("signing key %q not found in realm JWKS", kid)
	}
	return key, nil
}

func (v *Verifier) fetchJWKS(ctx context.Context, issuer string) (map[string]*rsa.PublicKey, error) {
	certsURL := strings.TrimRight(issuer, "/") + "/protocol/openid-connect/certs"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, certsURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := v.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch JWKS: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS endpoint returned %d", res.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return nil, err
	}
	out := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		e := 0
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		if e == 0 {
			e = int(binary.BigEndian.Uint32(append(make([]byte, 4-len(eBytes)), eBytes...)))
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}
	}
	return out, nil
}
