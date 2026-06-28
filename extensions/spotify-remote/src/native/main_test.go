package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	pendingPlaybackCall = nil
	pendingPlaybackCallID = 0
	rateLimitDisplayID = 0
}

func newNativeSpotifyAPITestApp(t *testing.T, client *http.Client) *app {
	t.Helper()
	base := t.TempDir()
	if err := writeJSON(filepath.Join(base, "data", "token.json"), &tokenFile{
		AccessToken: "access",
		TokenType:   "Bearer",
		ExpiresIn:   3600,
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	return &app{
		base:   base,
		cfg:    config{ClientID: "client-id"},
		client: client,
		quit:   make(chan struct{}),
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
	}); err != nil {
		t.Fatal(err)
	}

	a := &app{
		base:   base,
		cfg:    config{ClientID: "client-id"},
		client: srv.Client(),
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

// TestNative429DuringIdleSchedulesRetryPath verifies idle 429s use the scheduler path rather than the playback buffer.
func TestNative429DuringIdleSchedulesRetryPath(t *testing.T) {
	resetRateLimitStateForTest()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits > 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	a := newNativeSpotifyAPITestApp(t, srv.Client())

	_, err := a.spotifyAPIWithRetry(http.MethodGet, srv.URL+"/v1/me/player", nil, nil, true)
	if !errors.Is(err, errRateLimited) {
		t.Fatalf("spotifyAPIWithRetry error = %v, want errRateLimited", err)
	}
	rateLimitMu.Lock()
	pending := pendingPlaybackCall
	active := rateLimitActive
	rateLimitMu.Unlock()
	if pending != nil {
		t.Fatal("pendingPlaybackCall set during idle 429")
	}
	if !active {
		t.Fatal("rateLimitActive = false, want true")
	}
	time.Sleep(1300 * time.Millisecond)
	if hits != 2 {
		t.Fatalf("hits = %d, want scheduled retry hit count 2", hits)
	}
}

// TestNative429DuringActivePlaybackBuffersCall verifies active playback buffers the failed call without mutating the countdown/progress state.
func TestNative429DuringActivePlaybackBuffersCall(t *testing.T) {
	resetRateLimitStateForTest()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	a := newNativeSpotifyAPITestApp(t, srv.Client())
	a.hasState = true
	a.state = playback{IsPlaying: true, ProgressMS: 42000}

	_, err := a.spotifyAPIWithRetry(http.MethodGet, srv.URL+"/v1/me/player", nil, nil, true)
	if !errors.Is(err, errRateLimited) {
		t.Fatalf("spotifyAPIWithRetry error = %v, want errRateLimited", err)
	}
	rateLimitMu.Lock()
	pending := pendingPlaybackCall
	rateLimitMu.Unlock()
	if pending == nil {
		t.Fatal("pendingPlaybackCall nil, want buffered call")
	}
	if a.state.ProgressMS != 42000 {
		t.Fatalf("ProgressMS = %d, want countdown/progress unchanged", a.state.ProgressMS)
	}
}

// TestNativePendingPlaybackCallReplayedWhenStillActive verifies a buffered call is replayed after Retry-After when playback remains active.
func TestNativePendingPlaybackCallReplayedWhenStillActive(t *testing.T) {
	resetRateLimitStateForTest()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_playing":true}`))
	}))
	defer srv.Close()
	a := newNativeSpotifyAPITestApp(t, srv.Client())
	a.hasState = true
	a.state = playback{IsPlaying: true}

	_, err := a.spotifyAPIWithRetry(http.MethodGet, srv.URL+"/v1/me/player", nil, &playback{}, true)
	if !errors.Is(err, errRateLimited) {
		t.Fatalf("spotifyAPIWithRetry error = %v, want errRateLimited", err)
	}
	time.Sleep(1300 * time.Millisecond)
	if hits != 2 {
		t.Fatalf("hits = %d, want replay hit count 2", hits)
	}
	rateLimitMu.Lock()
	active := rateLimitActive
	rateLimitMu.Unlock()
	if active {
		t.Fatal("rateLimitActive = true, want false after replay success")
	}
}

// TestNativePendingPlaybackCallDiscardedWhenPlaybackEnds verifies a buffered call is discarded after Retry-After when playback has ended.
func TestNativePendingPlaybackCallDiscardedWhenPlaybackEnds(t *testing.T) {
	resetRateLimitStateForTest()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	a := newNativeSpotifyAPITestApp(t, srv.Client())
	a.hasState = true
	a.state = playback{IsPlaying: true}

	_, err := a.spotifyAPIWithRetry(http.MethodGet, srv.URL+"/v1/me/player", nil, nil, true)
	if !errors.Is(err, errRateLimited) {
		t.Fatalf("spotifyAPIWithRetry error = %v, want errRateLimited", err)
	}
	a.state.IsPlaying = false
	time.Sleep(1300 * time.Millisecond)
	if hits != 1 {
		t.Fatalf("hits = %d, want no replay after playback ended", hits)
	}
}

// TestKUALLoginMenuUsesWrapper verifies login actions use run-kual.sh so .new deployments run the newest binary.
func TestKUALLoginMenuUsesWrapper(t *testing.T) {
	type menuItem struct {
		Name   string     `json:"name"`
		Action string     `json:"action"`
		Params string     `json:"params"`
		Items  []menuItem `json:"items"`
	}
	for _, path := range []string{
		filepath.Join("..", "..", "menu.json"),
		filepath.Join("..", "..", "..", "spotifyremote", "menu.json"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var root struct {
			Items []menuItem `json:"items"`
		}
		if err := json.Unmarshal(raw, &root); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"Login":                          "/mnt/us/extensions/spotify-remote/run-kual.sh browser-login",
			"Manual Login URL":               "/mnt/us/extensions/spotify-remote/run-kual.sh login",
			"Finish Login From callback.txt": "/mnt/us/extensions/spotify-remote/run-kual.sh finish-login",
		}
		checked := map[string]bool{}
		for _, group := range root.Items {
			for _, item := range group.Items {
				if wantParams, ok := want[item.Name]; ok {
					checked[item.Name] = true
					if item.Action != "sh" {
						t.Fatalf("%s action in %s = %q, want sh", item.Name, path, item.Action)
					}
					if item.Params != wantParams {
						t.Fatalf("%s params in %s = %q, want %q", item.Name, path, item.Params, wantParams)
					}
				}
			}
		}
		for name := range want {
			if !checked[name] {
				t.Fatalf("%s missing menu item %q", path, name)
			}
		}
	}
}

// TestDeployCopiesKUALWrapper verifies USB deploy ships run-kual.sh with the KUAL extension files.
func TestDeployCopiesKUALWrapper(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "lib", "kindle.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"run-kual.sh"`) {
		t.Fatal("deploy script does not copy run-kual.sh")
	}
}
