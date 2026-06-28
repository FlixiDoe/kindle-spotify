// Package main implements the browser/server fallback for Kindle Spotify Remote.
// It targets environments where the Kindle extension can run a local HTTP
// server and render the remote through static browser assets instead of the
// native FBInk fullscreen UI. The file owns the OAuth PKCE callback flow,
// token persistence, Spotify Web API proxy endpoints, playback controls, cover
// proxying, and invalid_grant handling shared by the web frontend.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Spotify OAuth and default configuration values used by the browser fallback server.
const (
	// scopes lists the Spotify Web API permissions requested during PKCE login for reading playback state and sending player controls.
	scopes = "user-read-playback-state user-modify-playback-state user-read-currently-playing"
	// placeholderSpotifyClientID is the config-template value that marks an unconfigured Spotify application client ID.
	placeholderSpotifyClientID = "PASTE_SPOTIFY_CLIENT_ID_HERE"
)

// Package-level OAuth endpoint and sentinel errors shared by HTTP handlers and token refresh logic.
var (
	// spotifyTokenEndpoint is Spotify Accounts Service POST /api/token; tests may replace it to avoid live network calls.
	spotifyTokenEndpoint = "https://accounts.spotify.com/api/token"
	// errInvalidGrant marks Spotify Accounts HTTP 400 responses whose JSON error field is invalid_grant; callers delete data/token.json and require login.
	errInvalidGrant = errors.New("spotify invalid_grant")
	// errSessionExpired is the terminal refresh error returned to API handlers after invalid_grant invalidates the saved refresh token.
	errSessionExpired = errors.New("Session expired; press Login")
	// errRateLimited lets Spotify Web API callers distinguish HTTP 429 from other mapped Spotify errors.
	errRateLimited = errors.New("spotify rate limited")

	rateLimitMu                sync.Mutex
	rateLimitActive            bool
	rateLimitRetryAfterSeconds int
	rateLimitRetryAt           time.Time
	rateLimitNonRetryable      bool
)

// config describes data/config.json for the browser/server fallback runtime.
type config struct {
	ClientID       string `json:"client_id"`       // ClientID is the public Spotify application client ID used in PKCE requests; no client secret is stored.
	RedirectURI    string `json:"redirect_uri"`    // RedirectURI is the loopback callback URL registered with Spotify and served by callback.
	Port           int    `json:"port"`            // Port is the local HTTP server port and defaults to 8787.
	RefreshSeconds int    `json:"refresh_seconds"` // RefreshSeconds is exposed to the frontend so it can poll playback state at the configured cadence.
	ShowCover      bool   `json:"show_cover"`      // ShowCover enables the /api/cover proxy used by the browser UI to avoid direct image loading issues.
}

// tokenFile is the persisted Spotify token document stored at data/token.json.
type tokenFile struct {
	AccessToken  string `json:"access_token"`            // AccessToken is the bearer token sent to Spotify Web API endpoints until ExpiresAt passes.
	RefreshToken string `json:"refresh_token"`           // RefreshToken is the long-lived Spotify token used with grant_type=refresh_token when the access token expires.
	TokenType    string `json:"token_type"`              // TokenType is normally "Bearer" and is retained from Spotify's token response.
	Scope        string `json:"scope"`                   // Scope is Spotify's space-delimited granted scope list for diagnostics and future checks.
	ExpiresIn    int    `json:"expires_in"`              // ExpiresIn is Spotify's lifetime in seconds for the access token returned by /api/token.
	ExpiresAt    int64  `json:"expires_at"`              // ExpiresAt is the local Unix timestamp at which the token should be refreshed, with a safety margin.
	AuthorizedAt int64  `json:"authorized_at,omitempty"` // AuthorizedAt is the first successful authorization Unix timestamp, preserved across refreshes.
}

// oauthState is the short-lived PKCE login state stored at data/oauth.json between loginAPI and callback handling.
type oauthState struct {
	State        string `json:"state"`         // State is the CSRF token sent to Spotify authorize and compared with the callback state parameter.
	CodeVerifier string `json:"code_verifier"` // CodeVerifier is the high-entropy PKCE secret later posted to /api/token with the authorization code.
	CreatedAt    int64  `json:"created_at"`    // CreatedAt is the Unix timestamp when the login attempt was created for stale-state troubleshooting.
}

// app holds the server-wide runtime state for the fallback HTTP service.
type app struct {
	baseDir string       // baseDir is the extension root containing data, logs, and www directories.
	cfg     config       // cfg is the loaded server configuration and is mutated by configAPI before being saved.
	client  *http.Client // client is the shared HTTP client used for Spotify, OAuth token, and cover proxy requests.
}

// main starts the browser fallback HTTP server.
// It detects the extension root, configures file and stderr logging, loads config, registers static, OAuth, and Spotify API proxy routes, and blocks in ListenAndServe.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func main() {
	base := detectBaseDir()
	logFile := filepath.Join(base, "logs", "spotify-remote.log")
	_ = os.MkdirAll(filepath.Dir(logFile), 0755)
	if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
		defer f.Close()
	}
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	a := &app{baseDir: base, client: &http.Client{Timeout: 20 * time.Second}}
	if err := a.loadConfig(); err != nil {
		log.Fatalf("config error: %v", err)
	}
	if a.cfg.Port == 0 {
		a.cfg.Port = 8787
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", a.serveIndex)
	mux.HandleFunc("/style.css", a.serveStatic("style.css"))
	mux.HandleFunc("/app.js", a.serveStatic("app.js"))
	mux.HandleFunc("/callback", a.callback)
	mux.HandleFunc("/api/config", a.configAPI)
	mux.HandleFunc("/api/login", a.loginAPI)
	mux.HandleFunc("/api/manual-callback", a.manualCallbackAPI)
	mux.HandleFunc("/api/status", a.statusAPI)
	mux.HandleFunc("/api/devices", a.devicesAPI)
	mux.HandleFunc("/api/control", a.controlAPI)
	mux.HandleFunc("/api/cover", a.coverAPI)

	addr := "127.0.0.1:" + strconv.Itoa(a.cfg.Port)
	log.Printf("Spotify Remote listening on http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// detectBaseDir finds the extension root directory.
// It prefers the executable parent when running from bin, falls back to the working directory, and finally returns dot.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func detectBaseDir() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if filepath.Base(dir) == "bin" {
			return filepath.Dir(dir)
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// loadConfig reads and normalizes data/config.json.
// It starts from defaults, creates a template when the file is missing, applies fallback values, and returns read/write errors.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) loadConfig() error {
	a.cfg = defaultConfig()
	path := filepath.Join(a.baseDir, "data", "config.json")
	err := readJSON(path, &a.cfg)
	if os.IsNotExist(err) {
		return a.saveConfig()
	}
	return err
}

// saveConfig persists the current browser fallback configuration.
// It writes data/config.json with private permissions and returns directory, marshal, or write errors.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) saveConfig() error {
	return writeJSON(filepath.Join(a.baseDir, "data", "config.json"), &a.cfg, 0600)
}

// defaultConfig returns conservative native Kindle defaults.
// It sets OAuth, screen, touch, eips, and button values that match the target Kindle layout until data/config.json overrides them.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func defaultConfig() config {
	return config{
		ClientID:       placeholderSpotifyClientID,
		RedirectURI:    "http://127.0.0.1:8787/callback",
		Port:           8787,
		RefreshSeconds: 8,
		ShowCover:      true,
	}
}

// readJSON loads a JSON file into the supplied destination.
// It reads the whole file, treats empty or whitespace-only files as no-op defaults, and returns filesystem or JSON parse errors.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func readJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// writeJSON atomically prepares the parent directory and writes indented JSON.
// It creates directories, marshals with stable indentation, appends a newline, and stores private state with owner-readable permissions.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func writeJSON(path string, v any, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), perm)
}

// serveIndex serves the browser fallback index page.
// It only handles the root path and returns 404 for other paths before serving www/index.html.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.baseDir, "www", "index.html"))
}

// serveStatic returns a handler for one static browser asset.
// It closes over the asset name and serves it from the extension www directory.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) serveStatic(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(a.baseDir, "www", name))
	}
}

// configAPI reads or updates browser fallback configuration.
// GET returns the current config; POST decodes a client_id update, normalizes redirect_uri, saves config, and returns JSON status.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) configAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		respondJSON(w, a.cfg)
	case http.MethodPost:
		var in struct {
			ClientID string `json:"client_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			respondErr(w, http.StatusBadRequest, "Invalid config JSON")
			return
		}
		a.cfg.ClientID = strings.TrimSpace(in.ClientID)
		if a.cfg.RedirectURI == "" {
			a.cfg.RedirectURI = "http://127.0.0.1:8787/callback"
		}
		if err := a.saveConfig(); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, map[string]bool{"ok": true})
	default:
		respondErr(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// loginAPI creates a Spotify PKCE login request for the browser frontend.
// It validates configuration, generates verifier/challenge/state, writes data/oauth.json, builds the authorize URL, and returns it as JSON.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) loginAPI(w http.ResponseWriter, r *http.Request) {
	if !validClientID(a.cfg.ClientID) {
		respondErr(w, http.StatusBadRequest, "Spotify Client ID missing")
		return
	}
	// The PKCE code_verifier must be high entropy because Spotify later compares it with the S256 challenge.
	verifier := randomString(64)
	// Spotify requires the S256 code_challenge, which is SHA-256(verifier) encoded with unpadded base64url.
	challenge := pkceChallenge(verifier)
	// The OAuth state value binds the callback to this login attempt and prevents accepting an unrelated redirect.
	state := randomString(24)
	// data/oauth.json persists the verifier and state until the callback supplies the authorization code.
	if err := writeJSON(filepath.Join(a.baseDir, "data", "oauth.json"), oauthState{State: state, CodeVerifier: verifier, CreatedAt: time.Now().Unix()}, 0600); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// url.Values performs the percent-encoding Spotify expects for the authorization or token request.
	v := url.Values{}
	// client_id identifies the public Spotify application registered for this redirect URI.
	v.Set("client_id", a.cfg.ClientID)
	// response_type=code starts the Authorization Code with PKCE flow rather than an implicit-token flow.
	v.Set("response_type", "code")
	// redirect_uri must exactly match the URI registered in the Spotify developer dashboard.
	v.Set("redirect_uri", a.cfg.RedirectURI)
	// code_challenge_method=S256 tells Spotify to verify the SHA-256 PKCE challenge.
	v.Set("code_challenge_method", "S256")
	// code_challenge is safe to send to Spotify because the secret verifier remains only in data/oauth.json.
	v.Set("code_challenge", challenge)
	// state is echoed by Spotify on redirect and checked before exchanging the authorization code.
	v.Set("state", state)
	// scope requests the playback and, in the native UI, playlist permissions needed by the remote.
	v.Set("scope", scopes)
	respondJSON(w, map[string]string{"auth_url": "https://accounts.spotify.com/authorize?" + v.Encode()})
}

// validClientID reports whether a Spotify client ID has been configured.
// It rejects empty IDs and the template placeholder prefix.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func validClientID(id string) bool {
	return id != "" && !strings.HasPrefix(id, "PASTE_")
}

// randomString creates PKCE-safe random text.
// It reads cryptographic random bytes, encodes them with unpadded base64url, and falls back to a timestamp only if the Kindle random source fails.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func randomString(n int) string {
	b := make([]byte, n)
	// PKCE verifier and OAuth state generation depend on crypto/rand for unpredictable bytes.
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	// The random bytes become URL-safe text suitable for PKCE verifier and state parameters.
	return base64.RawURLEncoding.EncodeToString(b)
}

// pkceChallenge derives the Spotify S256 PKCE challenge from a verifier.
// It SHA-256 hashes the verifier bytes and base64url-encodes the digest without padding as required by RFC 7636.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func pkceChallenge(verifier string) string {
	// PKCE S256 hashes the exact verifier string bytes before base64url encoding.
	sum := sha256.Sum256([]byte(verifier))
	// RawURLEncoding intentionally omits padding because Spotify follows RFC 7636 base64url rules.
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// callback handles Spotify OAuth redirects in the browser fallback server.
// It rejects Spotify error callbacks, exchanges valid code/state pairs for tokens, and redirects back to the UI.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) callback(w http.ResponseWriter, r *http.Request) {
	if errText := r.URL.Query().Get("error"); errText != "" {
		http.Error(w, errText, http.StatusBadRequest)
		return
	}
	if err := a.exchangeCode(r.URL.Query().Get("code"), r.URL.Query().Get("state")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// manualCallbackAPI completes login from a pasted redirect URL or code.
// It decodes JSON input, parses the authorization code and state, exchanges them, and returns JSON success or error.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) manualCallbackAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respondErr(w, http.StatusBadRequest, "Invalid callback JSON")
		return
	}
	code, state, err := parseManualCode(in.Value)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.exchangeCode(code, state); err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, map[string]bool{"ok": true})
}

// parseManualCode extracts an OAuth code and optional state from browser input.
// It accepts a full redirect URL or raw code and returns validation errors for empty or malformed input.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func parseManualCode(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("Paste the redirect URL or code")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", errors.New("Invalid redirect URL")
		}
		return u.Query().Get("code"), u.Query().Get("state"), nil
	}
	return raw, "", nil
}

// exchangeCode exchanges a Spotify authorization code for tokens.
// It validates the saved PKCE state, posts authorization_code data to /api/token, stamps AuthorizedAt and ExpiresAt, and writes data/token.json.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) exchangeCode(code, state string) error {
	if code == "" {
		return errors.New("Missing authorization code")
	}
	var st oauthState
	// The token exchange must reload the original PKCE verifier and state from data/oauth.json.
	if err := readJSON(filepath.Join(a.baseDir, "data", "oauth.json"), &st); err != nil {
		return errors.New("Login state missing; press Login again")
	}
	// A mismatched OAuth state means the redirect does not belong to the saved login attempt.
	if st.State != "" && state != "" && st.State != state {
		return errors.New("OAuth state mismatch")
	}
	form := url.Values{}
	// Spotify token requests for public PKCE clients include client_id but no client secret.
	form.Set("client_id", a.cfg.ClientID)
	// grant_type=authorization_code exchanges the one-time callback code for access and refresh tokens.
	form.Set("grant_type", "authorization_code")
	// code is the short-lived authorization code received on the Spotify redirect.
	form.Set("code", code)
	// Spotify verifies redirect_uri during token exchange against the authorization request.
	form.Set("redirect_uri", a.cfg.RedirectURI)
	// code_verifier proves this process owns the secret used to derive the earlier code_challenge.
	form.Set("code_verifier", st.CodeVerifier)
	var tok tokenFile
	// POST /api/token returns the access token, refresh token, granted scopes, and expiry metadata.
	if err := a.spotifyForm(spotifyTokenEndpoint, form, "", &tok); err != nil {
		return err
	}
	// AuthorizedAt is written once after successful authorization and then preserved across refreshes.
	if tok.AuthorizedAt == 0 {
		// AuthorizedAt records when this login session was first established.
		tok.AuthorizedAt = time.Now().Unix()
	}
	// ExpiresAt subtracts a 60-second margin so requests refresh before Spotify rejects the bearer token.
	tok.ExpiresAt = time.Now().Unix() + int64(tok.ExpiresIn) - 60
	// data/token.json stores the newly authorized token set for future Spotify API calls.
	// The refreshed token replaces data/token.json so subsequent API calls use the new access token.
	if err := writeJSON(filepath.Join(a.baseDir, "data", "token.json"), &tok, 0600); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(a.baseDir, "data", "oauth.json"))
	return nil
}

// loadToken returns a valid Spotify access token, refreshing it when needed.
// It reads data/token.json, checks ExpiresAt, posts refresh_token to /api/token when stale, preserves refresh token/scope/AuthorizedAt, clears invalid_grant sessions, and rewrites token.json.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) loadToken() (tokenFile, error) {
	var tok tokenFile
	// loadToken reads data/token.json before every Spotify API call so controls survive process restarts.
	if err := readJSON(filepath.Join(a.baseDir, "data", "token.json"), &tok); err != nil {
		return tok, errors.New("Token missing; press Login")
	}
	if tok.AccessToken == "" {
		return tok, errors.New("Token missing; press Login")
	}
	// An expired token must be refreshed before the next Spotify Web API request.
	if time.Now().Unix() >= tok.ExpiresAt {
		if tok.RefreshToken == "" {
			return tok, errors.New("Token expired; press Login")
		}
		refreshed, err := a.refreshToken(tok.RefreshToken)
		if err != nil {
			// invalid_grant is terminal for this saved refresh token, so the only correct recovery is deleting token.json and logging in again.
			if errors.Is(err, errInvalidGrant) {
				// data/token.json is removed so later requests cannot keep retrying a revoked or expired refresh token.
				_ = a.clearToken()
				return tok, errSessionExpired
			}
			return tok, fmt.Errorf("Token expired: %w", err)
		}
		if refreshed.RefreshToken == "" {
			// Spotify may omit refresh_token on refresh; keep the previous one when that happens.
			refreshed.RefreshToken = tok.RefreshToken
		}
		if refreshed.Scope == "" {
			// Spotify may omit scope on refresh; keep the original granted scope list for later scope checks.
			refreshed.Scope = tok.Scope
		}
		// Refresh does not create a new login session, so preserve the original authorization timestamp.
		refreshed.AuthorizedAt = tok.AuthorizedAt
		tok = refreshed
		// ExpiresAt subtracts a 60-second margin so requests refresh before Spotify rejects the bearer token.
		tok.ExpiresAt = time.Now().Unix() + int64(tok.ExpiresIn) - 60
		// data/token.json stores the newly authorized token set for future Spotify API calls.
		// The refreshed token replaces data/token.json so subsequent API calls use the new access token.
		if err := writeJSON(filepath.Join(a.baseDir, "data", "token.json"), &tok, 0600); err != nil {
			return tok, err
		}
	}
	return tok, nil
}

// refreshToken exchanges a Spotify refresh token for a new token response.
// It posts grant_type=refresh_token to /api/token and returns the decoded token or spotifyForm error.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) refreshToken(refresh string) (tokenFile, error) {
	form := url.Values{}
	// Spotify token requests for public PKCE clients include client_id but no client secret.
	form.Set("client_id", a.cfg.ClientID)
	// grant_type=refresh_token asks Spotify Accounts for a new access token using the saved refresh token.
	form.Set("grant_type", "refresh_token")
	// refresh_token is the long-lived credential supplied by loadToken for renewal.
	form.Set("refresh_token", refresh)
	var tok tokenFile
	err := a.spotifyForm(spotifyTokenEndpoint, form, "", &tok)
	return tok, err
}

// clearToken deletes the persisted Spotify token file.
// It removes data/token.json and treats an already-missing file as success.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) clearToken() error {
	// clearToken deletes data/token.json after invalid_grant or explicit session cleanup.
	err := os.Remove(filepath.Join(a.baseDir, "data", "token.json"))
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return err
}

// spotifyForm posts a form-encoded request to Spotify Accounts.
// It sends application/x-www-form-urlencoded data to /api/token or another endpoint, applies an optional bearer token, decodes the JSON response, and maps non-2xx statuses.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) spotifyForm(endpoint string, form url.Values, bearer string, out any) error {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	// Spotify Accounts /api/token requires form-encoded OAuth parameters.
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return errors.New("Network blocked or Spotify unreachable")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return spotifyError(resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

// spotifyAPI sends an authorized Spotify Web API request.
// It loads or refreshes a bearer token, builds the request, sets JSON content type for request bodies, detects Spotify Web API 429 responses, decodes optional JSON responses, and maps non-2xx statuses through spotifyError.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) spotifyAPI(method, endpoint string, body io.Reader, out any) (int, error) {
	return a.spotifyAPIWithRetry(method, endpoint, body, out, true)
}

// spotifyAPIWithRetry sends an authorized Spotify Web API request with optional retry scheduling.
// It implements spotifyAPI and lets scheduleRetry perform its single retry without recursively scheduling another retry on a second 429.
// Parameters: method, endpoint, body, and out match spotifyAPI; allowSchedule controls whether a 429 starts scheduleRetry.
// Return values: the Spotify HTTP status and an error, including errRateLimited for HTTP 429.
// Error conditions: token load, request creation, network, JSON decoding, and mapped Spotify HTTP errors are returned to callers.
// Side effects: can perform Spotify HTTP calls, read/write token files through loadToken, and mutate rate-limit state.
func (a *app) spotifyAPIWithRetry(method, endpoint string, body io.Reader, out any, allowSchedule bool) (int, error) {
	tok, err := a.loadToken()
	if err != nil {
		return 0, err
	}
	var payload []byte
	if body != nil {
		var readErr error
		payload, readErr = io.ReadAll(body)
		if readErr != nil {
			return 0, readErr
		}
	}
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	// Spotify Web API endpoints authenticate with the current access token in the Bearer header.
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	if body != nil {
		// Spotify playback control endpoints expect JSON when a request body is present.
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, errors.New("Network blocked or Spotify API unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests && !isSpotifyTokenEndpoint(endpoint) {
		// Spotify returned 429; read Retry-After header (default 5 s if absent) and set errRateLimited, while explicitly excluding the token endpoint.
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		setRateLimit(retryAfter)
		retryFn := func() error {
			_, retryErr := a.spotifyAPIWithRetry(method, endpoint, bytes.NewReader(payload), out, false)
			return retryErr
		}
		if allowSchedule {
			// The first 429 outside OAuth starts one deferred retry; scheduled retries call this helper with allowSchedule=false.
			scheduleRetry(retryFn, time.Duration(retryAfter)*time.Second)
		}
		return resp.StatusCode, errRateLimited
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, spotifyError(resp.StatusCode, b)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, err
		}
	}
	clearRateLimit()
	return resp.StatusCode, nil
}

// parseRetryAfter converts Spotify's Retry-After header into seconds.
// It trims and parses the header as an integer second count, returning five seconds when Spotify omits the header or sends an invalid value.
// Parameters: header is the raw Retry-After response header.
// Return values: the positive retry delay in seconds.
// Error conditions: malformed headers are handled by returning the default.
// Side effects: none.
func parseRetryAfter(header string) int {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds <= 0 {
		return 5
	}
	return seconds
}

// isSpotifyTokenEndpoint reports whether an endpoint is Spotify Accounts /api/token.
// It compares the request endpoint with spotifyTokenEndpoint and the production Accounts token URL so token refresh errors keep their separate invalid_grant handling.
// Parameters: endpoint is the absolute URL being requested.
// Return values: true when the endpoint is the OAuth token endpoint, otherwise false.
// Error conditions: none.
// Side effects: none.
func isSpotifyTokenEndpoint(endpoint string) bool {
	return endpoint == spotifyTokenEndpoint || endpoint == "https://accounts.spotify.com/api/token"
}

// setRateLimit stores the current Spotify Web API rate-limit window.
// It records active=true, retryAfterSeconds, retryAt, and clears any prior non-retryable marker so callers can show the current wait.
// Parameters: retryAfterSeconds is the parsed Retry-After delay in seconds.
// Return values: none.
// Error conditions: none.
// Side effects: mutates package-level rate-limit state protected by rateLimitMu.
func setRateLimit(retryAfterSeconds int) {
	rateLimitMu.Lock()
	defer rateLimitMu.Unlock()
	// The package-level rate-limit state drives server 429 responses and browser countdown messages.
	rateLimitActive = true
	rateLimitRetryAfterSeconds = retryAfterSeconds
	rateLimitRetryAt = time.Now().Add(time.Duration(retryAfterSeconds) * time.Second)
	rateLimitNonRetryable = false
}

// clearRateLimit clears the current Spotify Web API rate-limit window.
// It marks active=false, resets retry metadata, and removes the non-retryable marker after a successful retry or later successful request.
// Parameters: none.
// Return values: none.
// Error conditions: none.
// Side effects: mutates package-level rate-limit state protected by rateLimitMu.
func clearRateLimit() {
	rateLimitMu.Lock()
	defer rateLimitMu.Unlock()
	// A successful Spotify Web API call proves the current 429 wait is over.
	rateLimitActive = false
	rateLimitRetryAfterSeconds = 0
	rateLimitRetryAt = time.Time{}
	rateLimitNonRetryable = false
}

// scheduleRetry retries one failed Spotify call after the provided wait.
// It starts a goroutine, waits retryAfter, invokes fn exactly once, marks a second errRateLimited result as non-retryable, and clears rate-limit state when fn succeeds.
// Parameters: fn is the failed Spotify operation recreated by the caller; retryAfter is the delay before the single retry.
// Return values: none.
// Error conditions: a second errRateLimited result sets non-retryable state, while other errors are logged and left for the original caller-visible error path.
// Side effects: starts one goroutine, invokes fn once, logs retry failures, and mutates package-level rate-limit state.
func scheduleRetry(fn func() error, retryAfter time.Duration) {
	go func() {
		// This goroutine performs exactly one deferred retry after Spotify's Retry-After delay.
		time.Sleep(retryAfter)
		err := fn()
		if errors.Is(err, errRateLimited) {
			rateLimitMu.Lock()
			// A second 429 is terminal for this scheduled retry; no further retry is started from this scheduler.
			rateLimitNonRetryable = true
			rateLimitMu.Unlock()
			return
		}
		if err != nil {
			log.Printf("scheduled Spotify retry failed: %v", err)
			return
		}
		clearRateLimit()
	}()
}

// spotifyError converts Spotify HTTP error responses into user-facing errors.
// It detects invalid_grant as a sentinel for terminal session expiry and maps common playback statuses such as 401, 403, 404, and 429.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func spotifyError(status int, body []byte) error {
	text := string(body)
	var wrapped struct {
		Error any `json:"error"`
	}
	_ = json.Unmarshal(body, &wrapped)
	// Spotify returns HTTP 400 with invalid_grant when an authorization code or refresh token is invalid, expired, or revoked.
	if status == http.StatusBadRequest && spotifyErrorCode(wrapped.Error) == "invalid_grant" {
		// errInvalidGrant lets token lifecycle callers distinguish terminal auth failure from transient HTTP errors.
		return errInvalidGrant
	}
	switch status {
	case http.StatusUnauthorized:
		return errors.New("Token expired")
	case http.StatusForbidden:
		if strings.Contains(strings.ToLower(text), "premium") {
			return errors.New("Premium required")
		}
		return errors.New("Spotify denied the request")
	case http.StatusNotFound:
		return errors.New("No active Spotify device")
	case http.StatusTooManyRequests:
		return errors.New("Spotify rate limit")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("Spotify API error HTTP %d", status)
	}
	return fmt.Errorf("Spotify API error HTTP %d: %.180s", status, text)
}

// spotifyErrorCode extracts a Spotify error code or message from decoded JSON.
// It handles both OAuth string errors and Web API object errors.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func spotifyErrorCode(v any) string {
	switch e := v.(type) {
	case string:
		return e
	case map[string]any:
		if msg, ok := e["message"].(string); ok {
			return msg
		}
	}
	return ""
}

// statusAPI proxies Spotify playback state to the browser frontend.
// It calls GET /v1/me/player, maps expired auth to a login_required response, maps 204 to 404, and returns Spotify JSON on success.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) statusAPI(w http.ResponseWriter, r *http.Request) {
	var state map[string]any
	status, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &state)
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			respondAuthExpired(w)
			return
		}
		if errors.Is(err, errRateLimited) {
			respondRateLimited(w)
			return
		}
		respondErr(w, http.StatusBadGateway, "Failed to get playback state: "+err.Error())
		return
	}
	if status == http.StatusNoContent || state == nil {
		respondErr(w, http.StatusNotFound, "No active Spotify device")
		return
	}
	respondJSON(w, state)
}

// devicesAPI proxies Spotify device discovery to the browser frontend.
// It calls GET /v1/me/player/devices and returns the decoded Spotify JSON or a mapped error.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) devicesAPI(w http.ResponseWriter, r *http.Request) {
	var devices map[string]any
	if _, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player/devices", nil, &devices); err != nil {
		if errors.Is(err, errSessionExpired) {
			respondAuthExpired(w)
			return
		}
		if errors.Is(err, errRateLimited) {
			respondRateLimited(w)
			return
		}
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respondJSON(w, devices)
}

// controlAPI maps browser playback commands to Spotify Web API endpoints.
// It decodes an action payload, builds the correct Spotify player endpoint and method, forwards the request, and returns JSON success.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) controlAPI(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respondErr(w, http.StatusBadRequest, "Invalid control JSON")
		return
	}
	var method, endpoint string
	var body io.Reader
	method = http.MethodPut
	switch stringValue(in, "action") {
	case "play":
		endpoint = "https://api.spotify.com/v1/me/player/play"
	case "pause":
		endpoint = "https://api.spotify.com/v1/me/player/pause"
	case "next":
		method = http.MethodPost
		endpoint = "https://api.spotify.com/v1/me/player/next"
	case "previous":
		method = http.MethodPost
		endpoint = "https://api.spotify.com/v1/me/player/previous"
	case "volume":
		endpoint = "https://api.spotify.com/v1/me/player/volume?volume_percent=" + strconv.Itoa(intValue(in, "volume_percent"))
	case "shuffle":
		endpoint = "https://api.spotify.com/v1/me/player/shuffle?state=" + strconv.FormatBool(boolValue(in, "state"))
	case "repeat":
		endpoint = "https://api.spotify.com/v1/me/player/repeat?state=" + url.QueryEscape(stringValue(in, "state"))
	case "transfer":
		endpoint = "https://api.spotify.com/v1/me/player"
		body = strings.NewReader(fmt.Sprintf(`{"device_ids":["%s"],"play":false}`, stringValue(in, "device_id")))
	default:
		respondErr(w, http.StatusBadRequest, "Unknown control")
		return
	}
	if _, err := a.spotifyAPI(method, endpoint, body, nil); err != nil {
		if errors.Is(err, errSessionExpired) {
			respondAuthExpired(w)
			return
		}
		if errors.Is(err, errRateLimited) {
			respondRateLimited(w)
			return
		}
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respondJSON(w, map[string]bool{"ok": true})
}

// stringValue safely extracts a string from a generic JSON map.
// It returns an empty string when the key is missing or not a string.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func stringValue(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// boolValue safely extracts a bool from a generic JSON map.
// It returns false when the key is missing or not a bool.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func boolValue(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

// intValue safely extracts an integer from a generic JSON map.
// It accepts float64 values produced by encoding/json and native ints, otherwise returning zero.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func intValue(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

// coverAPI proxies Spotify cover images for the browser frontend.
// It validates ShowCover and HTTPS URLs, downloads the image, preserves or infers content type, and streams at most 512 KiB.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) coverAPI(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.ShowCover {
		http.NotFound(w, r)
		return
	}
	raw := r.URL.Query().Get("url")
	if raw == "" || !strings.HasPrefix(raw, "https://") {
		http.NotFound(w, r)
		return
	}
	resp, err := a.client.Get(raw)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.NotFound(w, r)
		return
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(raw))
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 512*1024))
}

// respondJSON writes a JSON response.
// It sets application/json and encodes the supplied value, ignoring encode errors because handlers have no recovery path after writing.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func respondJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// respondErr writes a structured JSON error response.
// It logs the status and message, sets application/json, writes the status code, and encodes an error object.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func respondErr(w http.ResponseWriter, status int, msg string) {
	log.Printf("HTTP %d: %s", status, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// respondRateLimited writes the standard browser fallback Spotify rate-limit response.
// It snapshots package-level rate-limit state, sets HTTP 429 and Retry-After, and returns JSON that the browser uses for a countdown without issuing its own retry.
// Parameters: w is the HTTP response writer for the local browser fallback request.
// Return values: none.
// Error conditions: JSON encoding errors are ignored after headers are written.
// Side effects: writes HTTP response headers/body and logs the rate-limit status.
func respondRateLimited(w http.ResponseWriter) {
	rateLimitMu.Lock()
	retryAfter := rateLimitRetryAfterSeconds
	if rateLimitActive && !rateLimitRetryAt.IsZero() {
		retryAfter = int(time.Until(rateLimitRetryAt).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
	}
	rateLimitMu.Unlock()
	if retryAfter <= 0 {
		retryAfter = 5
	}
	message := fmt.Sprintf("Spotify rate limited - retrying in %ds", retryAfter)
	log.Printf("HTTP %d: %s", http.StatusTooManyRequests, message)
	w.Header().Set("Content-Type", "application/json")
	// Retry-After mirrors Spotify's wait so the browser can disable controls without polling during the wait.
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":       "rate_limited",
		"retry_after": retryAfter,
		"message":     message,
	})
}

// respondAuthExpired writes the standard expired-session response.
// It returns HTTP 401 with login_required=true so the browser frontend can prompt for a fresh Spotify login.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func respondAuthExpired(w http.ResponseWriter) {
	log.Printf("HTTP %d: session expired; login required", http.StatusUnauthorized)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":          "Session expired; please login again",
		"login_required": true,
	})
}
