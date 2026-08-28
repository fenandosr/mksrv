// SPDX-License-Identifier: Apache-2.0

// Command configd is the Cloud-IT VPN configuration broker. See
// internal/configd.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fenandosr/mksrv/internal/configd"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	listen := envOr("CONFIGD_LISTEN", ":8090")
	kid := os.Getenv("CONFIGD_SIGNING_KID")
	keyRaw := readSecret("CONFIGD_SIGNING_KEY")
	hsURL := envOr("CONFIGD_HEADSCALE_URL", "http://mksrv-headscale:8080")
	hsKey := strings.TrimSpace(string(readSecret("CONFIGD_HEADSCALE_APIKEY")))
	tenantsRaw := readSecret("CONFIGD_TENANTS")

	if kid == "" || len(keyRaw) == 0 || hsKey == "" || len(tenantsRaw) == 0 {
		log.Error("configd requires CONFIGD_SIGNING_KID, CONFIGD_SIGNING_KEY, CONFIGD_HEADSCALE_APIKEY, and CONFIGD_TENANTS")
		os.Exit(1)
	}

	keyBytes, err := configd.ParsePrivateKey(keyRaw)
	if err != nil {
		log.Error("signing key", "error", err)
		os.Exit(1)
	}
	signer, err := configd.NewSigner(kid, keyBytes)
	if err != nil {
		log.Error("signing key", "error", err)
		os.Exit(1)
	}
	cfg, err := configd.ParseConfig(tenantsRaw)
	if err != nil {
		log.Error("parse CONFIGD_TENANTS", "error", err)
		os.Exit(1)
	}

	server := configd.NewServer(cfg, signer, configd.NewHeadscaleClient(hsURL, hsKey), log)
	httpServer := &http.Server{
		Addr:              listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Info("configd listening", "addr", listen, "tenants", len(cfg.Tenants))
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// readSecret reads KEY_FILE if set, otherwise KEY.
func readSecret(key string) []byte {
	if path := strings.TrimSpace(os.Getenv(key + "_FILE")); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
	}
	return []byte(os.Getenv(key))
}
