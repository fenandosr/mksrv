// SPDX-License-Identifier: Apache-2.0

package keycloak

// The Admin API representations mksrv sends. The reconcile code and
// `mksrv tenant contract` both build them here, so what a local analogue of a
// realm is told cannot drift from what the live realm is given.

// RealmRepresentation is the body that creates a missing realm.
func RealmRepresentation(spec RealmSpec) map[string]any {
	body := map[string]any{
		"realm":                 spec.Realm,
		"enabled":               true,
		"displayName":           spec.DisplayName,
		"loginWithEmailAllowed": true,
		"registrationAllowed":   false,
		"sslRequired":           "external",
	}
	if spec.LoginTheme != "" {
		body["loginTheme"] = spec.LoginTheme
	}
	return body
}

// PasswordResetSettings are the realm settings applied together with SMTP: the
// "Forgot password?" link is pointless without it.
func PasswordResetSettings() map[string]any {
	return map[string]any{
		"resetPasswordAllowed":  true,
		"loginWithEmailAllowed": true,
	}
}

// SettingsRepresentation is the realm update for declared settings; empty when
// nothing is declared.
func SettingsRepresentation(st *RealmSettings) map[string]any {
	body := map[string]any{}
	if st == nil {
		return body
	}
	if st.AccessTokenLifespan > 0 {
		body["accessTokenLifespan"] = st.AccessTokenLifespan
	}
	if st.SSOSessionIdle > 0 {
		body["ssoSessionIdleTimeout"] = st.SSOSessionIdle
	}
	if st.RefreshTokenRotation != nil {
		body["revokeRefreshToken"] = *st.RefreshTokenRotation
		body["refreshTokenMaxReuse"] = 0
	}
	return body
}

// ClientRepresentation is the body a realm reconcile creates or updates a
// client with.
func ClientRepresentation(cs ClientSpec) map[string]any {
	return map[string]any{
		"clientId":                  cs.ClientID,
		"enabled":                   true,
		"publicClient":              cs.Public,
		"standardFlowEnabled":       true,
		"directAccessGrantsEnabled": cs.DirectGrants,
		"serviceAccountsEnabled":    !cs.Public,
		"redirectUris":              cs.RedirectURIs,
		"webOrigins":                webOrigins(cs),
		"attributes":                clientAttributes(cs),
	}
}

// singleClientRepresentation is the narrower body EnsureClient sends.
func singleClientRepresentation(cs ClientSpec) map[string]any {
	return map[string]any{
		"clientId":                  cs.ClientID,
		"enabled":                   true,
		"publicClient":              cs.Public,
		"standardFlowEnabled":       true,
		"directAccessGrantsEnabled": false,
		"redirectUris":              cs.RedirectURIs,
		"webOrigins":                webOrigins(cs),
		"attributes":                map[string]string{"pkce.code.challenge.method": "S256"},
	}
}

func webOrigins(cs ClientSpec) []string {
	if cs.WebOrigins == nil {
		return []string{"+"}
	}
	return cs.WebOrigins
}

// Mappers are the mksrv-* protocol mappers a realm reconcile gives a client.
func Mappers(cs ClientSpec) []map[string]any { return desiredMappers(cs) }

// AudienceMapper is the `mksrv-audience` mapper EnsureClient adds, which puts
// the client's own id in the `aud` claim.
func AudienceMapper(clientID string) map[string]any {
	return map[string]any{
		"name":           "mksrv-audience",
		"protocol":       "openid-connect",
		"protocolMapper": "oidc-audience-mapper",
		"config": map[string]string{
			"included.client.audience": clientID,
			"access.token.claim":       "true",
			"id.token.claim":           "true",
		},
	}
}
