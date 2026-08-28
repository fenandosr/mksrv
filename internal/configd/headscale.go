// SPDX-License-Identifier: Apache-2.0

package configd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HeadscaleClient mints pre-auth keys through the Headscale HTTP API.
type HeadscaleClient struct {
	base   string
	apiKey string
	http   *http.Client
}

// NewHeadscaleClient targets a Headscale API base URL (e.g.
// http://mksrv-headscale:8080) with an API key.
func NewHeadscaleClient(baseURL, apiKey string) *HeadscaleClient {
	return &HeadscaleClient{
		base:   strings.TrimRight(baseURL, "/"),
		apiKey: apiKey,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

// PreAuthKey creates a single-use, non-ephemeral pre-auth key for a user
// (by name) that expires after ttl.
func (h *HeadscaleClient) PreAuthKey(ctx context.Context, user string, ttl time.Duration) (string, error) {
	userID, err := h.userID(ctx, user)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{
		"user":       userID,
		"reusable":   false,
		"ephemeral":  false,
		"expiration": time.Now().Add(ttl).UTC().Format(time.RFC3339),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+"/api/v1/preauthkey", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+h.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := h.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("headscale preauthkey request: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("headscale preauthkey failed (%d): %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		PreAuthKey struct {
			Key string `json:"key"`
		} `json:"preAuthKey"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode preauthkey response: %w", err)
	}
	if out.PreAuthKey.Key == "" {
		return "", fmt.Errorf("headscale returned an empty pre-auth key")
	}
	return out.PreAuthKey.Key, nil
}

// userID resolves a Headscale user name to its numeric id (the form the
// preauthkey API requires).
func (h *HeadscaleClient) userID(ctx context.Context, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+"/api/v1/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+h.apiKey)
	res, err := h.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("headscale user list: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("headscale user list failed (%d): %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Users []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"users"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	for _, u := range out.Users {
		if u.Name == name {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("headscale user %q not found", name)
}
