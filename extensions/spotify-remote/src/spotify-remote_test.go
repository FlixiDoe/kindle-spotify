package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadTokenInvalidGrantClearsToken(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	oldEndpoint := spotifyTokenEndpoint
	spotifyTokenEndpoint = srv.URL + "/api/token"
	defer func() { spotifyTokenEndpoint = oldEndpoint }()

	base := t.TempDir()
	tokenPath := filepath.Join(base, "data", "token.json")
	if err := writeJSON(tokenPath, &tokenFile{
		AccessToken:  "expired-access",
		RefreshToken: "expired-refresh",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(-time.Hour).Unix(),
		AuthorizedAt: time.Now().Add(-180 * 24 * time.Hour).Unix(),
	}, 0600); err != nil {
		t.Fatal(err)
	}

	a := &app{
		baseDir: base,
		cfg:     config{ClientID: "client-id"},
		client:  srv.Client(),
	}
	_, err := a.loadToken()
	if !errors.Is(err, errSessionExpired) {
		t.Fatalf("loadToken error = %v, want errSessionExpired", err)
	}
	if hits != 1 {
		t.Fatalf("refresh attempts = %d, want 1", hits)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("token file still exists or stat failed unexpectedly: %v", err)
	}
}
