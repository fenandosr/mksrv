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
