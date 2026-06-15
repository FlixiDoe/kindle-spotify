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
	"time"
)

const scopes = "user-read-playback-state user-modify-playback-state user-read-currently-playing"

type config struct {
	ClientID       string `json:"client_id"`
	RedirectURI    string `json:"redirect_uri"`
	Port           int    `json:"port"`
	RefreshSeconds int    `json:"refresh_seconds"`
	ShowCover      bool   `json:"show_cover"`
}

type tokenFile struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
}

type oauthState struct {
	State        string `json:"state"`
	CodeVerifier string `json:"code_verifier"`
	CreatedAt    int64  `json:"created_at"`
}

type app struct {
	baseDir string
	cfg     config
	client  *http.Client
}

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

func (a *app) loadConfig() error {
	a.cfg = config{RedirectURI: "http://127.0.0.1:8787/callback", Port: 8787, RefreshSeconds: 8, ShowCover: true}
	return readJSON(filepath.Join(a.baseDir, "data", "config.json"), &a.cfg)
}

func (a *app) saveConfig() error {
	return writeJSON(filepath.Join(a.baseDir, "data", "config.json"), &a.cfg, 0600)
}

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

func (a *app) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.baseDir, "www", "index.html"))
}

func (a *app) serveStatic(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(a.baseDir, "www", name))
	}
}

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

func (a *app) loginAPI(w http.ResponseWriter, r *http.Request) {
	if !validClientID(a.cfg.ClientID) {
		respondErr(w, http.StatusBadRequest, "Spotify Client ID missing")
		return
	}
	verifier := randomString(64)
	challenge := pkceChallenge(verifier)
	state := randomString(24)
	if err := writeJSON(filepath.Join(a.baseDir, "data", "oauth.json"), oauthState{State: state, CodeVerifier: verifier, CreatedAt: time.Now().Unix()}, 0600); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	v := url.Values{}
	v.Set("client_id", a.cfg.ClientID)
	v.Set("response_type", "code")
	v.Set("redirect_uri", a.cfg.RedirectURI)
	v.Set("code_challenge_method", "S256")
	v.Set("code_challenge", challenge)
	v.Set("state", state)
	v.Set("scope", scopes)
	respondJSON(w, map[string]string{"auth_url": "https://accounts.spotify.com/authorize?" + v.Encode()})
}

func validClientID(id string) bool {
	return id != "" && !strings.HasPrefix(id, "PASTE_")
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

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

func (a *app) exchangeCode(code, state string) error {
	if code == "" {
		return errors.New("Missing authorization code")
	}
	var st oauthState
	if err := readJSON(filepath.Join(a.baseDir, "data", "oauth.json"), &st); err != nil {
		return errors.New("Login state missing; press Login again")
	}
	if st.State != "" && state != "" && st.State != state {
		return errors.New("OAuth state mismatch")
	}
	form := url.Values{}
	form.Set("client_id", a.cfg.ClientID)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", a.cfg.RedirectURI)
	form.Set("code_verifier", st.CodeVerifier)
	var tok tokenFile
	if err := a.spotifyForm("https://accounts.spotify.com/api/token", form, "", &tok); err != nil {
		return err
	}
	tok.ExpiresAt = time.Now().Unix() + int64(tok.ExpiresIn) - 60
	if err := writeJSON(filepath.Join(a.baseDir, "data", "token.json"), &tok, 0600); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(a.baseDir, "data", "oauth.json"))
	return nil
}

func (a *app) loadToken() (tokenFile, error) {
	var tok tokenFile
	if err := readJSON(filepath.Join(a.baseDir, "data", "token.json"), &tok); err != nil {
		return tok, errors.New("Token missing; press Login")
	}
	if tok.AccessToken == "" {
		return tok, errors.New("Token missing; press Login")
	}
	if time.Now().Unix() >= tok.ExpiresAt {
		if tok.RefreshToken == "" {
			return tok, errors.New("Token expired; press Login")
		}
		refreshed, err := a.refreshToken(tok.RefreshToken)
		if err != nil {
			return tok, fmt.Errorf("Token expired: %w", err)
		}
		if refreshed.RefreshToken == "" {
			refreshed.RefreshToken = tok.RefreshToken
		}
		tok = refreshed
		tok.ExpiresAt = time.Now().Unix() + int64(tok.ExpiresIn) - 60
		if err := writeJSON(filepath.Join(a.baseDir, "data", "token.json"), &tok, 0600); err != nil {
			return tok, err
		}
	}
	return tok, nil
}

func (a *app) refreshToken(refresh string) (tokenFile, error) {
	form := url.Values{}
	form.Set("client_id", a.cfg.ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refresh)
	var tok tokenFile
	err := a.spotifyForm("https://accounts.spotify.com/api/token", form, "", &tok)
	return tok, err
}

func (a *app) spotifyForm(endpoint string, form url.Values, bearer string, out any) error {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
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

func (a *app) spotifyAPI(method, endpoint string, body io.Reader, out any) (int, error) {
	tok, err := a.loadToken()
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, errors.New("Network blocked or Spotify API unreachable")
	}
	defer resp.Body.Close()
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
	return resp.StatusCode, nil
}

func spotifyError(status int, body []byte) error {
	text := string(body)
	var wrapped struct {
		Error any `json:"error"`
	}
	_ = json.Unmarshal(body, &wrapped)
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

func (a *app) statusAPI(w http.ResponseWriter, r *http.Request) {
	var state map[string]any
	status, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &state)
	if err != nil {
		respondErr(w, http.StatusBadGateway, "Failed to get playback state: "+err.Error())
		return
	}
	if status == http.StatusNoContent || state == nil {
		respondErr(w, http.StatusNotFound, "No active Spotify device")
		return
	}
	respondJSON(w, state)
}

func (a *app) devicesAPI(w http.ResponseWriter, r *http.Request) {
	var devices map[string]any
	if _, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player/devices", nil, &devices); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respondJSON(w, devices)
}

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
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respondJSON(w, map[string]bool{"ok": true})
}

func stringValue(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func boolValue(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

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

func respondJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func respondErr(w http.ResponseWriter, status int, msg string) {
	log.Printf("HTTP %d: %s", status, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
