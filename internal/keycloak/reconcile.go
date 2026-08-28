// SPDX-License-Identifier: Apache-2.0

package keycloak

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// RealmSpec describes the desired state of a tenant realm.
type RealmSpec struct {
	Realm       string
	DisplayName string
	Groups      []string
	Clients     []ClientSpec
}

// ClientSpec describes an OIDC client.
type ClientSpec struct {
	ClientID     string
	Public       bool
	RedirectURIs []string
	// WebOrigins defaults to "+" (derive from redirect URIs) when nil.
	WebOrigins []string
}

// RealmResult reports what EnsureRealm changed.
type RealmResult struct {
	Realm          string   `json:"realm"`
	RealmCreated   bool     `json:"realm_created"`
	GroupsCreated  []string `json:"groups_created"`
	ClientsCreated []string `json:"clients_created"`
}

// EnsureRealm creates the realm, groups, and clients that are missing. It never
// deletes anything and only sets display name on creation.
func (c *Client) EnsureRealm(ctx context.Context, spec RealmSpec) (RealmResult, error) {
	result := RealmResult{Realm: spec.Realm}

	status, _ := c.do(ctx, http.MethodGet, "/realms/"+spec.Realm, nil, nil)
	if status == http.StatusNotFound {
		body := map[string]any{
			"realm":                 spec.Realm,
			"enabled":               true,
			"displayName":           spec.DisplayName,
			"loginWithEmailAllowed": true,
			"registrationAllowed":   false,
			"sslRequired":           "external",
		}
		if _, err := c.do(ctx, http.MethodPost, "/realms", body, nil); err != nil {
			return result, err
		}
		result.RealmCreated = true
	} else if status != http.StatusOK {
		return result, fmt.Errorf("check realm %s: unexpected status %d", spec.Realm, status)
	}

	existingGroups, err := c.groupNames(ctx, spec.Realm)
	if err != nil {
		return result, err
	}
	for _, name := range spec.Groups {
		if existingGroups[name] {
			continue
		}
		if _, err := c.do(ctx, http.MethodPost, "/realms/"+spec.Realm+"/groups", map[string]any{"name": name}, nil); err != nil {
			return result, err
		}
		result.GroupsCreated = append(result.GroupsCreated, name)
	}

	existingClients, err := c.clientUUIDs(ctx, spec.Realm)
	if err != nil {
		return result, err
	}
	for _, cs := range spec.Clients {
		origins := cs.WebOrigins
		if origins == nil {
			origins = []string{"+"}
		}
		body := map[string]any{
			"clientId":                  cs.ClientID,
			"enabled":                   true,
			"publicClient":              cs.Public,
			"standardFlowEnabled":       true,
			"directAccessGrantsEnabled": false,
			"serviceAccountsEnabled":    !cs.Public,
			"redirectUris":              cs.RedirectURIs,
			"webOrigins":                origins,
			"attributes": map[string]string{
				"pkce.code.challenge.method": "S256",
			},
		}
		if uuid, ok := existingClients[cs.ClientID]; ok {
			if _, err := c.do(ctx, http.MethodPut, "/realms/"+spec.Realm+"/clients/"+uuid, body, nil); err != nil {
				return result, err
			}
			continue
		}
		if _, err := c.do(ctx, http.MethodPost, "/realms/"+spec.Realm+"/clients", body, nil); err != nil {
			return result, err
		}
		result.ClientsCreated = append(result.ClientsCreated, cs.ClientID)
	}
	return result, nil
}

// EnsureClient creates or updates a single OIDC client in an existing realm and
// returns its secret (empty for public clients).
func (c *Client) EnsureClient(ctx context.Context, realm string, spec ClientSpec) (string, error) {
	origins := spec.WebOrigins
	if origins == nil {
		origins = []string{"+"}
	}
	body := map[string]any{
		"clientId":                  spec.ClientID,
		"enabled":                   true,
		"publicClient":              spec.Public,
		"standardFlowEnabled":       true,
		"directAccessGrantsEnabled": false,
		"redirectUris":              spec.RedirectURIs,
		"webOrigins":                origins,
		"attributes":                map[string]string{"pkce.code.challenge.method": "S256"},
	}
	existing, err := c.clientUUIDs(ctx, realm)
	if err != nil {
		return "", err
	}
	uuid, ok := existing[spec.ClientID]
	if ok {
		if _, err := c.do(ctx, http.MethodPut, "/realms/"+realm+"/clients/"+uuid, body, nil); err != nil {
			return "", err
		}
	} else {
		if _, err := c.do(ctx, http.MethodPost, "/realms/"+realm+"/clients", body, nil); err != nil {
			return "", err
		}
		if existing, err = c.clientUUIDs(ctx, realm); err != nil {
			return "", err
		}
		uuid = existing[spec.ClientID]
	}
	if spec.Public || uuid == "" {
		return "", nil
	}
	var secret struct {
		Value string `json:"value"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/realms/"+realm+"/clients/"+uuid+"/client-secret", nil, &secret); err != nil {
		return "", err
	}
	return secret.Value, nil
}

// UserSpec is one declarative realm user.
type UserSpec struct {
	Email   string
	Name    string
	Groups  []string
	Enabled bool
}

// UsersResult reports a user reconciliation.
type UsersResult struct {
	Realm   string   `json:"realm"`
	Created []string `json:"created"`
	Updated []string `json:"updated"`
}

// EnsureUsers creates missing users and updates group membership and enabled
// state. It never deletes users (pruning is a separate, explicit action).
func (c *Client) EnsureUsers(ctx context.Context, realm string, users []UserSpec) (UsersResult, error) {
	result := UsersResult{Realm: realm}
	groupIDs, err := c.groupIDs(ctx, realm)
	if err != nil {
		return result, err
	}
	for _, u := range users {
		id, existed, err := c.ensureUser(ctx, realm, u)
		if err != nil {
			return result, err
		}
		if existed {
			result.Updated = append(result.Updated, u.Email)
		} else {
			result.Created = append(result.Created, u.Email)
		}
		for _, g := range u.Groups {
			gid, ok := groupIDs[g]
			if !ok {
				return result, fmt.Errorf("user %s references unknown group %q in realm %s", u.Email, g, realm)
			}
			if _, err := c.do(ctx, http.MethodPut,
				fmt.Sprintf("/realms/%s/users/%s/groups/%s", realm, id, gid), map[string]any{}, nil); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func (c *Client) ensureUser(ctx context.Context, realm string, u UserSpec) (string, bool, error) {
	var found []struct {
		ID string `json:"id"`
	}
	if _, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/realms/%s/users?exact=true&email=%s", realm, url.QueryEscape(u.Email)), nil, &found); err != nil {
		return "", false, err
	}
	first, last := splitName(u.Name)
	body := map[string]any{
		"username":      u.Email,
		"email":         u.Email,
		"emailVerified": true,
		"enabled":       u.Enabled,
		"firstName":     first,
		"lastName":      last,
	}
	if len(found) > 0 {
		if _, err := c.do(ctx, http.MethodPut, "/realms/"+realm+"/users/"+found[0].ID, body, nil); err != nil {
			return "", true, err
		}
		return found[0].ID, true, nil
	}
	if _, err := c.do(ctx, http.MethodPost, "/realms/"+realm+"/users", body, nil); err != nil {
		return "", false, err
	}
	if _, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/realms/%s/users?exact=true&email=%s", realm, url.QueryEscape(u.Email)), nil, &found); err != nil {
		return "", false, err
	}
	if len(found) == 0 {
		return "", false, fmt.Errorf("user %s not found after creation", u.Email)
	}
	return found[0].ID, false, nil
}

func (c *Client) groupNames(ctx context.Context, realm string) (map[string]bool, error) {
	ids, err := c.groupIDs(ctx, realm)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(ids))
	for name := range ids {
		names[name] = true
	}
	return names, nil
}

func (c *Client) groupIDs(ctx context.Context, realm string) (map[string]string, error) {
	var groups []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/realms/"+realm+"/groups?max=200", nil, &groups); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(groups))
	for _, g := range groups {
		out[g.Name] = g.ID
	}
	return out, nil
}

func (c *Client) clientUUIDs(ctx context.Context, realm string) (map[string]string, error) {
	var clients []struct {
		ID       string `json:"id"`
		ClientID string `json:"clientId"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/realms/"+realm+"/clients?max=200", nil, &clients); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(clients))
	for _, cl := range clients {
		out[cl.ClientID] = cl.ID
	}
	return out, nil
}

func splitName(name string) (string, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ""
	}
	parts := strings.Fields(name)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.Join(parts[1:], " ")
}
