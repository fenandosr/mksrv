// SPDX-License-Identifier: Apache-2.0

package keycloak

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnsureRealmCreatesWhenAbsent(t *testing.T) {
	t.Parallel()
	var createdRealm, createdGroups, createdClients int
	realmExists := false

	mux := http.NewServeMux()
	mux.HandleFunc("/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "test-token"})
	})
	mux.HandleFunc("/admin/realms/acme", func(w http.ResponseWriter, _ *http.Request) {
		if !realmExists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"realm": "acme"})
	})
	mux.HandleFunc("/admin/realms", func(w http.ResponseWriter, _ *http.Request) {
		createdRealm++
		realmExists = true
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/admin/realms/acme/groups", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			createdGroups++
			w.WriteHeader(http.StatusCreated)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/admin/realms/acme/clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			createdClients++
			w.WriteHeader(http.StatusCreated)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := New(server.URL)
	if err := c.Login(context.Background(), "admin", "pw"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	res, err := c.EnsureRealm(context.Background(), RealmSpec{
		Realm:  "acme",
		Groups: []string{"apps", "both"},
		Clients: []ClientSpec{
			{ClientID: "cloud-it-vpn-desktop", Public: true},
			{ClientID: "configd"},
		},
	})
	if err != nil {
		t.Fatalf("EnsureRealm() error = %v", err)
	}
	if !res.RealmCreated || createdRealm != 1 || createdGroups != 2 || createdClients != 2 {
		t.Fatalf("result=%+v realm=%d groups=%d clients=%d", res, createdRealm, createdGroups, createdClients)
	}

	// Second call: realm now exists, nothing new is created.
	res, err = c.EnsureRealm(context.Background(), RealmSpec{Realm: "acme", Groups: []string{"apps", "both"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.RealmCreated || createdRealm != 1 {
		t.Fatalf("idempotency broken: %+v createdRealm=%d", res, createdRealm)
	}
}

func TestEnsureRealmAddsHardcodedClaimMapper(t *testing.T) {
	t.Parallel()
	var mapperPosts []map[string]any
	mapperExists := false

	mux := http.NewServeMux()
	mux.HandleFunc("/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "t"})
	})
	mux.HandleFunc("/admin/realms/acme", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"realm": "acme"})
	})
	mux.HandleFunc("/admin/realms/acme/groups", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/admin/realms/acme/clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "u1", "clientId": "cloud-it-vpn-desktop"}})
	})
	mux.HandleFunc("/admin/realms/acme/clients/u1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/admin/realms/acme/clients/u1/protocol-mappers/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mapperPosts = append(mapperPosts, body)
			mapperExists = true
			w.WriteHeader(http.StatusCreated)
			return
		}
		if mapperExists {
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "mksrv-role"}})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := New(server.URL)
	if err := c.Login(context.Background(), "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	spec := RealmSpec{
		Realm: "acme",
		Clients: []ClientSpec{{
			ClientID:        "cloud-it-vpn-desktop",
			Public:          true,
			HardcodedClaims: map[string]string{"role": "bitabit"},
		}},
	}
	if _, err := c.EnsureRealm(context.Background(), spec); err != nil {
		t.Fatalf("EnsureRealm() error = %v", err)
	}
	if len(mapperPosts) != 1 {
		t.Fatalf("want 1 mapper POST, got %d", len(mapperPosts))
	}
	cfg, _ := mapperPosts[0]["config"].(map[string]any)
	if mapperPosts[0]["protocolMapper"] != "oidc-hardcoded-claim-mapper" || cfg["claim.name"] != "role" || cfg["claim.value"] != "bitabit" {
		t.Fatalf("unexpected mapper body: %+v", mapperPosts[0])
	}

	// Second apply: mapper now present, no duplicate POST.
	if _, err := c.EnsureRealm(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(mapperPosts) != 1 {
		t.Fatalf("idempotency broken: %d mapper POSTs", len(mapperPosts))
	}
}

func TestEnsureRealmAddsGroupsMapper(t *testing.T) {
	t.Parallel()
	var mapperPosts []map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "t"})
	})
	mux.HandleFunc("/admin/realms/acme", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"realm": "acme"})
	})
	mux.HandleFunc("/admin/realms/acme/groups", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/admin/realms/acme/clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "u1", "clientId": "openbao"}})
	})
	mux.HandleFunc("/admin/realms/acme/clients/u1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/admin/realms/acme/clients/u1/protocol-mappers/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mapperPosts = append(mapperPosts, body)
			w.WriteHeader(http.StatusCreated)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := New(server.URL)
	if err := c.Login(context.Background(), "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnsureRealm(context.Background(), RealmSpec{
		Realm:   "acme",
		Clients: []ClientSpec{{ClientID: "openbao", GroupsClaim: true}},
	}); err != nil {
		t.Fatalf("EnsureRealm() error = %v", err)
	}
	if len(mapperPosts) != 1 || mapperPosts[0]["protocolMapper"] != "oidc-group-membership-mapper" {
		t.Fatalf("want 1 group-membership mapper POST, got %+v", mapperPosts)
	}
	cfg, _ := mapperPosts[0]["config"].(map[string]any)
	if cfg["claim.name"] != "groups" || cfg["full.path"] != "false" {
		t.Fatalf("unexpected groups mapper config: %+v", cfg)
	}
}

func TestEnsureRealmGrantsTeamManagement(t *testing.T) {
	t.Parallel()
	var rolePost []map[string]string

	mux := http.NewServeMux()
	mux.HandleFunc("/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "t"})
	})
	mux.HandleFunc("/admin/realms/acme", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"realm": "acme"})
	})
	mux.HandleFunc("/admin/realms/acme/groups", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "g-admin", "name": "admin"}, {"id": "g-dev", "name": "dev"}})
	})
	mux.HandleFunc("/admin/realms/acme/clients", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "rm", "clientId": "realm-management"}})
	})
	mux.HandleFunc("/admin/realms/acme/clients/rm/roles", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"id": "r1", "name": "manage-users"}, {"id": "r2", "name": "query-users"},
			{"id": "r3", "name": "query-groups"}, {"id": "r4", "name": "view-users"},
		})
	})
	mux.HandleFunc("/admin/realms/acme/groups/g-admin/role-mappings/clients/rm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&rolePost)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := New(server.URL)
	if err := c.Login(context.Background(), "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnsureRealm(context.Background(), RealmSpec{
		Realm:      "acme",
		Groups:     []string{"admin", "dev", "apps", "vpn"},
		AdminGroup: "admin",
	}); err != nil {
		t.Fatalf("EnsureRealm() error = %v", err)
	}
	if len(rolePost) != 4 {
		t.Fatalf("want 4 roles assigned to the admin group, got %+v", rolePost)
	}
}

func TestEnsureClientReturnsSecret(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "t"})
	})
	created := false
	mux.HandleFunc("/admin/realms/master/clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			created = true
			w.WriteHeader(http.StatusCreated)
			return
		}
		if created {
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "abc", "clientId": "grafana"}})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/admin/realms/master/clients/abc/client-secret", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": "s3cr3t"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := New(server.URL)
	if err := c.Login(context.Background(), "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	secret, err := c.EnsureClient(context.Background(), "master", ClientSpec{ClientID: "grafana"})
	if err != nil {
		t.Fatalf("EnsureClient() error = %v", err)
	}
	if secret != "s3cr3t" {
		t.Fatalf("secret = %q", secret)
	}
}

func TestDoReAuthenticatesOn401(t *testing.T) {
	t.Parallel()
	logins := 0
	firstCall := true
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		logins++
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	})
	mux.HandleFunc("/admin/realms/acme/groups", func(w http.ResponseWriter, _ *http.Request) {
		if firstCall {
			firstCall = false
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "g1", "name": "dev"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := New(server.URL)
	if err := c.Login(context.Background(), "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	got, err := c.groupIDs(context.Background(), "acme")
	if err != nil {
		t.Fatalf("groupIDs after 401 retry: %v", err)
	}
	if _, ok := got["dev"]; !ok {
		t.Fatalf("groups = %v", got)
	}
	if logins != 2 {
		t.Fatalf("want 2 logins (initial + re-auth), got %d", logins)
	}
}
