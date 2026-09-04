// SPDX-License-Identifier: Apache-2.0

// Package keycloak is a minimal Keycloak Admin REST client. mksrv uses it to
// reconcile one realm per tenant, its groups, and the OIDC clients consumed by
// Cloud-IT VPN and configd, plus declarative tenant users.
package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one Keycloak instance.
type Client struct {
	base     string
	http     *http.Client
	token    string
	user     string
	password string
}

// New creates a client for baseURL (e.g. https://auth.example.com).
func New(baseURL string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// Login obtains an admin access token from the master realm using the
// admin-cli public client and the direct-grant flow.
func (c *Client) Login(ctx context.Context, username, password string) error {
	c.user, c.password = username, password
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {username},
		"password":   {password},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/realms/master/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak login: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("keycloak login failed (%d): %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		return fmt.Errorf("keycloak login: no access token in response")
	}
	c.token = token.AccessToken
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, in any, out any) (int, error) {
	var blob []byte
	if in != nil {
		var err error
		if blob, err = json.Marshal(in); err != nil {
			return 0, err
		}
	}

	send := func() (*http.Response, error) {
		var reader io.Reader
		if in != nil {
			reader = bytes.NewReader(blob)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+"/admin"+path, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return c.http.Do(req)
	}

	res, err := send()
	if err != nil {
		return 0, err
	}
	// The admin token is short-lived; on a 401 re-authenticate once and retry.
	if res.StatusCode == http.StatusUnauthorized && c.user != "" {
		res.Body.Close()
		if err := c.Login(ctx, c.user, c.password); err != nil {
			return http.StatusUnauthorized, fmt.Errorf("keycloak re-login: %w", err)
		}
		if res, err = send(); err != nil {
			return 0, err
		}
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 400 {
		return res.StatusCode, fmt.Errorf("keycloak %s %s: %d %s", method, path, res.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return res.StatusCode, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return res.StatusCode, nil
}
