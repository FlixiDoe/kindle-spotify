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

func resetRateLimitStateForTest() {
	rateLimitMu.Lock()
	defer rateLimitMu.Unlock()
	rateLimitActive = false
	rateLimitRetryAfterSeconds = 0
	rateLimitRetryAt = time.Time{}
	rateLimitNonRetryable = false
}

func newSpotifyAPITestApp(t *testing.T, client *http.Client) *app {
	t.Helper()
	base := t.TempDir()
	if err := writeJSON(filepath.Join(base, "data", "token.json"), &tokenFile{
		AccessToken: "access",
		TokenType:   "Bearer",
		ExpiresIn:   3600,
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}, 0600); err != nil {
		t.Fatal(err)
	}
	return &app{
		baseDir: base,
		cfg:     config{ClientID: "client-id"},
		client:  client,
	}
}

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

// TestSpotifyAPI429SetsRateLimitState verifies that the central browser wrapper returns errRateLimited and records Retry-After metadata.
func TestSpotifyAPI429SetsRateLimitState(t *testing.T) {
	resetRateLimitStateForTest()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	a := newSpotifyAPITestApp(t, srv.Client())

	_, err := a.spotifyAPIWithRetry(http.MethodGet, srv.URL+"/v1/me/player", nil, nil, false)
	if !errors.Is(err, errRateLimited) {
		t.Fatalf("spotifyAPIWithRetry error = %v, want errRateLimited", err)
	}
	rateLimitMu.Lock()
	active, retryAfter, retryAt := rateLimitActive, rateLimitRetryAfterSeconds, rateLimitRetryAt
	rateLimitMu.Unlock()
	if !active || retryAfter != 7 || time.Until(retryAt) <= 0 {
		t.Fatalf("rate limit state active=%v retryAfter=%d retryAt=%v", active, retryAfter, retryAt)
	}
}

// TestSpotifyAPI429DefaultRetryAfter verifies that a missing Retry-After header uses the five-second default.
func TestSpotifyAPI429DefaultRetryAfter(t *testing.T) {
	resetRateLimitStateForTest()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	a := newSpotifyAPITestApp(t, srv.Client())

	_, err := a.spotifyAPIWithRetry(http.MethodGet, srv.URL+"/v1/me/player", nil, nil, false)
	if !errors.Is(err, errRateLimited) {
		t.Fatalf("spotifyAPIWithRetry error = %v, want errRateLimited", err)
	}
	rateLimitMu.Lock()
	retryAfter := rateLimitRetryAfterSeconds
	rateLimitMu.Unlock()
	if retryAfter != 5 {
		t.Fatalf("retryAfter = %d, want 5", retryAfter)
	}
}

// TestScheduleRetrySecond429SetsNonRetryable verifies that the one-shot scheduler stops after a second 429.
func TestScheduleRetrySecond429SetsNonRetryable(t *testing.T) {
	resetRateLimitStateForTest()
	scheduleRetry(func() error { return errRateLimited }, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	rateLimitMu.Lock()
	nonRetryable := rateLimitNonRetryable
	rateLimitMu.Unlock()
	if !nonRetryable {
		t.Fatal("rateLimitNonRetryable = false, want true")
	}
}

// TestSpotifyFormTokenEndpoint429NotIntercepted verifies that OAuth token endpoint 429s keep the normal token error path.
func TestSpotifyFormTokenEndpoint429NotIntercepted(t *testing.T) {
	resetRateLimitStateForTest()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	oldEndpoint := spotifyTokenEndpoint
	spotifyTokenEndpoint = srv.URL + "/api/token"
	defer func() { spotifyTokenEndpoint = oldEndpoint }()
	a := &app{baseDir: t.TempDir(), cfg: config{ClientID: "client-id"}, client: srv.Client()}

	err := a.spotifyForm(spotifyTokenEndpoint, nil, "", &tokenFile{})
	if errors.Is(err, errRateLimited) {
		t.Fatalf("spotifyForm error = %v, did not want errRateLimited", err)
	}
	rateLimitMu.Lock()
	active := rateLimitActive
	rateLimitMu.Unlock()
	if active {
		t.Fatal("rateLimitActive = true, want false")
	}
}

// TestScheduleRetrySuccessClearsRateLimit verifies that a successful scheduled retry clears stored 429 state.
func TestScheduleRetrySuccessClearsRateLimit(t *testing.T) {
	resetRateLimitStateForTest()
	setRateLimit(5)
	scheduleRetry(func() error { return nil }, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	rateLimitMu.Lock()
	active := rateLimitActive
	rateLimitMu.Unlock()
	if active {
		t.Fatal("rateLimitActive = true, want false")
	}
}
