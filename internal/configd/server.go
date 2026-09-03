// SPDX-License-Identifier: Apache-2.0

package configd

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// TenantEntry is one tenant's brokering configuration.
type TenantEntry struct {
	Issuer        string    `json:"issuer"` // <keycloak>/realms/<realm>
	Tenant        string    `json:"tenant"`
	DisplayName   string    `json:"displayName"`
	Primary       string    `json:"primary"`
	LogoDataURI   string    `json:"logoDataUri"`
	HeadscaleUser string    `json:"headscaleUser"`
	ControlURL    string    `json:"controlUrl"`
	Forwards      []Forward `json:"forwards"`
	UpdateFeedURL string    `json:"updateFeedUrl"`
	MinVersion    string    `json:"minVersion"`
}

// Config is configd's tenant roster.
type Config struct {
	Tenants []TenantEntry `json:"tenants"`
}

// ParseConfig decodes the JSON tenant roster.
func ParseConfig(raw []byte) (Config, error) {
	var cfg Config
	err := json.Unmarshal(raw, &cfg)
	return cfg, err
}

// Server answers GET /v1/clientconfig.
type Server struct {
	byIssuer  map[string]TenantEntry
	signer    *Signer
	verifier  *Verifier
	headscale *HeadscaleClient
	now       func() time.Time
	log       *slog.Logger
}

// NewServer wires a configd HTTP handler.
func NewServer(cfg Config, signer *Signer, hs *HeadscaleClient, log *slog.Logger) *Server {
	byIssuer := make(map[string]TenantEntry, len(cfg.Tenants))
	for _, t := range cfg.Tenants {
		byIssuer[strings.TrimRight(t.Issuer, "/")] = t
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		byIssuer:  byIssuer,
		signer:    signer,
		verifier:  NewVerifier(),
		headscale: hs,
		now:       time.Now,
		log:       log,
	}
}

// Handler returns the configd routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/clientconfig", s.clientConfig)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

var nodeNameRe = regexp.MustCompile(`[^a-z0-9-]+`)

func (s *Server) clientConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.now().UTC()

	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" || token == r.Header.Get("Authorization") {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}

	allowed := make(map[string]bool, len(s.byIssuer))
	for issuer := range s.byIssuer {
		allowed[issuer] = true
	}
	claims, err := s.verifier.Verify(ctx, token, allowed, now)
	if err != nil {
		s.log.Info("token rejected", "error", err)
		writeError(w, http.StatusUnauthorized, "token verification failed")
		return
	}

	tenant, ok := s.byIssuer[strings.TrimRight(claims.Issuer, "/")]
	if !ok {
		writeError(w, http.StatusForbidden, "no entitlement for this realm")
		return
	}

	if !claims.VPNEntitled() {
		s.log.Info("token has no VPN group", "tenant", tenant.Tenant, "sub", claims.Subject, "groups", claims.Groups)
		writeError(w, http.StatusForbidden, "your account is not in a VPN-enabled group (vpn, dev, or admin)")
		return
	}

	preauth, err := s.headscale.PreAuthKey(ctx, tenant.HeadscaleUser, 15*time.Minute)
	if err != nil {
		s.log.Error("preauth key", "tenant", tenant.Tenant, "error", err)
		writeError(w, http.StatusBadGateway, "could not mint a mesh key")
		return
	}

	cfg := ClientConfig{
		Version:  1,
		IssuedAt: now.Unix(),
		Tenant: Tenant{
			ID:          tenant.Tenant,
			DisplayName: tenant.DisplayName,
			Branding:    Branding{Primary: tenant.Primary, LogoDataURI: tenant.LogoDataURI},
		},
		Tailnet: Tailnet{
			ControlURL: tenant.ControlURL,
			PreauthKey: preauth,
			NodeName:   nodeName(tenant.Tenant, r.Header.Get("X-Device-Name")),
			Ephemeral:  false,
		},
		Forwards: tenant.Forwards,
		Policy: Policy{
			Reconnect:         Reconnect{BackoffSec: []int{1, 2, 5, 10, 30, 60}},
			IdleDisconnectMin: 0,
			KillSwitch:        false,
		},
		Update:    Update{FeedURL: tenant.UpdateFeedURL, MinVersion: tenant.MinVersion},
		Telemetry: Telemetry{Enabled: false, Endpoint: ""},
	}

	compact, err := s.signer.Sign(cfg)
	if err != nil {
		s.log.Error("sign clientconfig", "error", err)
		writeError(w, http.StatusInternalServerError, "could not sign configuration")
		return
	}
	w.Header().Set("Content-Type", "application/jose")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(compact))
	s.log.Info("clientconfig issued", "tenant", tenant.Tenant, "sub", claims.Subject)
}

func nodeName(tenant, deviceHeader string) string {
	slug := strings.ToLower(strings.TrimSpace(deviceHeader))
	slug = nodeNameRe.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "device"
	}
	name := tenant + "-" + slug
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.Trim(name, "-")
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
