// Package main implements the native Kindle Spotify Remote runtime.
// It targets jailbroken Kindle devices where Go can shell out to FBInk, eips,
// lipc-set-prop, and Linux /dev/input event devices to provide a fullscreen
// touch UI without a browser. The file owns the Kindle-specific KUAL login
// workflow, Spotify PKCE authorization, token refresh, playback controls,
// session expiry handling, and display/input loops used by the extension.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Spotify OAuth, display, input, and runtime defaults used throughout the native remote.
const (
	// scopes lists the Spotify Web API permissions requested during PKCE login; playback state and control require user-read-playback-state, user-read-currently-playing, and user-modify-playback-state, while playlist context names require the playlist-read-* scopes.
	scopes = "user-read-playback-state user-modify-playback-state user-read-currently-playing playlist-read-private playlist-read-collaborative"
	// placeholderSpotifyClientID is the config-template value that marks an unconfigured Spotify application client ID.
	placeholderSpotifyClientID = "PASTE_SPOTIFY_CLIENT_ID_HERE"
)

// Package-level OAuth endpoints and sentinel errors shared by KUAL, FBInk UI, and background refresh paths.
var (
	// spotifyTokenEndpoint is Spotify Accounts Service POST /api/token; tests may replace it to avoid live network calls.
	spotifyTokenEndpoint = "https://accounts.spotify.com/api/token"
	// errInvalidGrant marks Spotify Accounts HTTP 400 responses whose JSON error field is invalid_grant; callers clear data/token.json and force a fresh login.
	errInvalidGrant = errors.New("spotify invalid_grant")
	// errSessionExpired is the user-facing terminal refresh error used after invalid_grant proves the stored refresh token can no longer be used.
	errSessionExpired = errors.New("Session abgelaufen - bitte erneut einloggen")
	// errRateLimited lets Spotify Web API callers distinguish HTTP 429 from other mapped Spotify errors.
	errRateLimited = errors.New("spotify rate limited")

	rateLimitMu                sync.Mutex
	rateLimitActive            bool
	rateLimitRetryAfterSeconds int
	rateLimitRetryAt           time.Time
	rateLimitNonRetryable      bool
	pendingPlaybackCall        func() error
	pendingPlaybackCallID      int64
	rateLimitDisplayID         int64
)

// config describes data/config.json for the native Kindle runtime.
type config struct {
	ClientID          string `json:"client_id"`            // ClientID is the public Spotify application client ID used in PKCE requests; no client secret is stored on the Kindle.
	Redirect          string `json:"redirect_uri"`         // Redirect is the loopback callback URL registered with Spotify and used by KUAL/browser login flows.
	Port              int    `json:"port"`                 // Port is the local loopback HTTP port for OAuth callbacks and defaults to 8787.
	RefreshSec        int    `json:"refresh_seconds"`      // RefreshSec controls the polling interval for playback state in the eips UI loop.
	ScreenWidth       int    `json:"screen_width"`         // ScreenWidth is the Kindle framebuffer width in pixels used to map touch coordinates to UI regions.
	ScreenHeight      int    `json:"screen_height"`        // ScreenHeight is the Kindle framebuffer height in pixels used to map touch coordinates to UI regions.
	TouchMinX         int    `json:"touch_min_x"`          // TouchMinX is the configured raw input minimum for X when kernel ABS metadata is unavailable or disabled.
	TouchMaxX         int    `json:"touch_max_x"`          // TouchMaxX is the configured raw input maximum for X when kernel ABS metadata is unavailable or disabled.
	TouchMinY         int    `json:"touch_min_y"`          // TouchMinY is the configured raw input minimum for Y when kernel ABS metadata is unavailable or disabled.
	TouchMaxY         int    `json:"touch_max_y"`          // TouchMaxY is the configured raw input maximum for Y when kernel ABS metadata is unavailable or disabled.
	TouchSwapXY       bool   `json:"touch_swap_xy"`        // TouchSwapXY swaps raw axes for Kindle models whose touch controller orientation differs from the framebuffer.
	TouchInvertX      bool   `json:"touch_invert_x"`       // TouchInvertX mirrors normalized X after scaling for panels mounted in the opposite horizontal direction.
	TouchInvertY      bool   `json:"touch_invert_y"`       // TouchInvertY mirrors normalized Y after scaling for panels mounted in the opposite vertical direction.
	TouchUseKernelAbs *bool  `json:"touch_use_kernel_abs"` // TouchUseKernelAbs selects EVIOCGABS calibration from the kernel when true or nil, and forces config ranges when false.
	EipsColWidth      int    `json:"eips_col_width"`       // EipsColWidth converts framebuffer X pixels into eips text columns for button hit labels.
	EipsRowHeight     int    `json:"eips_row_height"`      // EipsRowHeight converts framebuffer Y pixels into eips text rows for button hit labels.
	ButtonTop         int    `json:"button_top"`           // ButtonTop is the first button's top pixel position in the eips fallback UI.
	ButtonHeight      int    `json:"button_height"`        // ButtonHeight is the vertical pixel span for each touch button in the eips fallback UI.
	ButtonGap         int    `json:"button_gap"`           // ButtonGap is the vertical pixel gap inserted between eips fallback UI buttons.
}

// tokenFile is the persisted Spotify token document stored at data/token.json.
type tokenFile struct {
	AccessToken  string `json:"access_token"`            // AccessToken is the bearer token sent to Spotify Web API endpoints until ExpiresAt passes.
	RefreshToken string `json:"refresh_token"`           // RefreshToken is the long-lived Spotify token used with grant_type=refresh_token when the access token expires.
	TokenType    string `json:"token_type"`              // TokenType is normally "Bearer" and is retained for completeness from Spotify's token response.
	Scope        string `json:"scope"`                   // Scope is Spotify's space-delimited granted scope list and is checked before fetching private playlist names.
	ExpiresIn    int    `json:"expires_in"`              // ExpiresIn is Spotify's lifetime in seconds for the access token returned by /api/token.
	ExpiresAt    int64  `json:"expires_at"`              // ExpiresAt is the local Unix timestamp at which the access token should be considered stale, with a safety margin.
	AuthorizedAt int64  `json:"authorized_at,omitempty"` // AuthorizedAt is the first successful authorization Unix timestamp, preserved across refreshes for session age diagnostics.
}

// oauthState is the short-lived PKCE login state stored at data/oauth.json between authorization URL creation and callback handling.
type oauthState struct {
	State        string `json:"state"`         // State is the CSRF token sent to Spotify authorize and compared with the callback state parameter.
	CodeVerifier string `json:"code_verifier"` // CodeVerifier is the high-entropy PKCE secret later posted to /api/token with the authorization code.
	CreatedAt    int64  `json:"created_at"`    // CreatedAt is the Unix timestamp when the login attempt was created for stale-state troubleshooting.
}

// artist models the subset of Spotify artist JSON needed for display.
type artist struct {
	Name string `json:"name"` // Name is the human-readable artist name shown in KUAL and FBInk views.
}

// albumImage models a Spotify album artwork candidate.
type albumImage struct {
	URL    string `json:"url"`    // URL is the HTTPS image URL downloaded before FBInk rendering.
	Height int    `json:"height"` // Height is Spotify's image height metadata in pixels.
	Width  int    `json:"width"`  // Width is Spotify's image width metadata in pixels and is used to choose a Kindle-friendly cover size.
}

// album models the subset of Spotify album JSON used in now-playing output.
type album struct {
	Name   string       `json:"name"`   // Name is the album title or release title displayed under the track.
	Images []albumImage `json:"images"` // Images are Spotify-provided cover artwork variants ordered by Spotify's response.
}

// track models the current Spotify item returned by GET /v1/me/player.
type track struct {
	Name       string   `json:"name"`        // Name is the current track title.
	Artists    []artist `json:"artists"`     // Artists are the credited artists joined for display.
	Album      album    `json:"album"`       // Album contains cover art and release metadata for the current track.
	DurationMS int      `json:"duration_ms"` // DurationMS is the total track duration in milliseconds.
}

// device models the active Spotify playback device.
type device struct {
	ID            string `json:"id"`             // ID is Spotify's device identifier used by transfer playback calls.
	Name          string `json:"name"`           // Name is the device label shown to the user.
	Type          string `json:"type"`           // Type is Spotify's device class, such as Computer, Smartphone, or Speaker.
	IsActive      bool   `json:"is_active"`      // IsActive reports whether Spotify currently routes playback to this device.
	VolumePercent int    `json:"volume_percent"` // VolumePercent is the device volume used for display and +/-10 adjustments.
}

// playbackContext models the optional album, artist, playlist, or collection context for the current track.
type playbackContext struct {
	Type         string            `json:"type"`          // Type identifies the context kind returned by Spotify, such as playlist, album, artist, or collection.
	Href         string            `json:"href"`          // Href is the Spotify Web API URL that can be queried for a display name when scopes permit it.
	URI          string            `json:"uri"`           // URI is the spotify:* identifier used as a fallback context reference.
	ExternalURLs map[string]string `json:"external_urls"` // ExternalURLs may include the public Spotify URL used as another fallback reference.
}

// playback models the response body from GET /v1/me/player.
type playback struct {
	Device       device          `json:"device"`        // Device is the active playback device and volume state.
	Shuffle      bool            `json:"shuffle_state"` // Shuffle reports Spotify's current shuffle setting.
	Repeat       string          `json:"repeat_state"`  // Repeat is Spotify's repeat mode: off, track, or context.
	ProgressMS   int             `json:"progress_ms"`   // ProgressMS is the current playback position in milliseconds.
	IsPlaying    bool            `json:"is_playing"`    // IsPlaying determines whether play/pause actions should call pause or play.
	CurrentTrack track           `json:"item"`          // CurrentTrack is the currently playing track object.
	Context      playbackContext `json:"context"`       // Context is the album, playlist, artist, or collection containing the current track.
}

// uiButton describes one rectangular touch target in the eips fallback interface.
type uiButton struct {
	Label string // Label is the text rendered for the button and recorded in tap diagnostics.
	X1    int    // X1 is the inclusive left edge in normalized framebuffer pixels.
	Y1    int    // Y1 is the inclusive top edge in normalized framebuffer pixels.
	X2    int    // X2 is the inclusive right edge in normalized framebuffer pixels.
	Y2    int    // Y2 is the inclusive bottom edge in normalized framebuffer pixels.
	Do    func() // Do is the callback invoked when a normalized tap lands inside the rectangle.
}

// uiTouchZone describes one fixed hotspot in the FBInk fullscreen now-playing layout.
type uiTouchZone struct {
	Action string // Action is the internal control command emitted to the FBInk UI loop.
	Label  string // Label is the diagnostic name used when logging hit testing.
	X1     int    // X1 is the inclusive left edge in framebuffer pixels for the fixed FBInk layout.
	Y1     int    // Y1 is the inclusive top edge in framebuffer pixels for the fixed FBInk layout.
	X2     int    // X2 is the inclusive right edge in framebuffer pixels for the fixed FBInk layout.
	Y2     int    // Y2 is the inclusive bottom edge in framebuffer pixels for the fixed FBInk layout.
}

// app holds the mutable process state for the native Kindle remote.
type app struct {
	base                    string        // base is the extension root containing data, logs, bin, and www directories.
	cfg                     config        // cfg is the loaded runtime configuration, normalized during startup.
	client                  *http.Client  // client is the shared HTTP client used for Spotify, cover downloads, and OAuth token calls.
	status                  string        // status is the latest user-facing status line rendered to eips or FBInk.
	err                     string        // err is the latest user-facing error line rendered to eips or written for KUAL.
	state                   playback      // state is the last successful playback snapshot from GET /v1/me/player.
	hasState                bool          // hasState reports whether state currently contains a successful playback snapshot.
	buttons                 []uiButton    // buttons is the current eips fallback hit-test table, rebuilt on draw.
	mu                      sync.Mutex    // mu protects status, err, state, hasState, buttons, lastDraw, and lastTap.
	lastDraw                time.Time     // lastDraw throttles eips redraws to avoid excessive Kindle framebuffer refreshes.
	lastAction              time.Time     // lastAction debounces touch events from noisy input devices.
	lastTap                 string        // lastTap stores the most recent tap diagnostic displayed in the eips UI.
	quit                    chan struct{} // quit is closed or signaled to stop loops and clear the display.
	rateLimitPreviousStatus string        // rateLimitPreviousStatus stores the status line restored after a rate-limit retry succeeds.
	rateLimitStatusActive   bool          // rateLimitStatusActive reports whether the rate-limit countdown owns the status line.
}

// main initializes logging, configuration, routing mode, background loops, and the Kindle display lifecycle.
// It chooses KUAL, FBInk fullscreen UI, or eips fallback mode from os.Args, starts callback/touch/refresh goroutines where needed, and waits for quit before clearing the screen.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func main() {
	a := &app{
		base:   detectBaseDir(),
		client: &http.Client{Timeout: 20 * time.Second},
		status: "Starting",
		quit:   make(chan struct{}),
	}
	a.setupLog()
	if err := a.loadConfig(); err != nil {
		a.err = "Config error: " + err.Error()
	}
	if len(os.Args) > 1 && os.Args[1] == "kual" {
		action := "status"
		if len(os.Args) > 2 {
			action = os.Args[2]
		}
		a.runKUAL(action)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "ui" {
		a.runFBInkUI()
		return
	}
	go a.callbackServer()
	go a.touchLoop()
	a.draw()
	go a.refreshLoop()
	<-a.quit
	eipsClear()
}

// runFBInkUI runs the fullscreen FBInk now-playing interface.
// It grabs touch devices, polls queued actions, dispatches playback controls, redraws the now-playing screen every eight seconds, and releases touch input on quit.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) runFBInkUI() {
	log.Printf("Starting FBInk UI")
	a.status = "Starting UI"
	tapCh := make(chan string, 8)
	done := make(chan struct{})
	stopTouch := func() {
		select {
		case <-done:
		default:
			close(done)
			time.Sleep(250 * time.Millisecond)
		}
	}
	go a.grabTouchLoop(tapCh, done)
	defer stopTouch()

	nextDraw := time.Now()
	for {
		select {
		case action := <-tapCh:
			log.Printf("UI action: %s", action)
			if action == "quit" {
				stopTouch()
				a.fbinkExitMessage()
				return
			}
			a.uiControl(action)
			nextDraw = time.Now()
		default:
		}
		if time.Now().After(nextDraw) {
			a.drawFBInkNowPlaying()
			nextDraw = time.Now().Add(8 * time.Second)
		}
		time.Sleep(120 * time.Millisecond)
	}
}

// fbinkExitMessage paints the final FBInk shutdown message.
// It clears the Kindle display, writes two centered text lines, and logs that the fullscreen UI is returning to Kindle.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) fbinkExitMessage() {
	eipsClear()
	a.fbinkText(3, 12, "Closing Spotify Remote")
	a.fbinkText(2, 16, "Returning to Kindle...")
	log.Printf("FBInk UI exit message drawn")
}

// uiControl maps a fullscreen UI action string to the matching Spotify control.
// It dispatches play/pause, track navigation, volume, shuffle, or repeat commands through the KUAL helper methods.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) uiControl(action string) {
	switch action {
	case "playpause":
		a.kualPlayPause()
	case "next":
		a.kualControl(http.MethodPost, "https://api.spotify.com/v1/me/player/next", nil, "Next")
	case "prev":
		a.kualControl(http.MethodPost, "https://api.spotify.com/v1/me/player/previous", nil, "Previous")
	case "volup":
		a.kualVolume(10)
	case "voldown":
		a.kualVolume(-10)
	case "shuffle":
		a.kualToggleShuffle()
	case "repeat":
		a.kualToggleRepeat()
	}
}

// drawFBInkNowPlaying fetches Spotify playback state and renders the fullscreen Kindle now-playing layout.
// It calls GET /v1/me/player, handles no-device and expired-session states, downloads cover art when available, then updates FBInk/eips regions for title, controls, progress, volume, shuffle, repeat, and device.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) drawFBInkNowPlaying() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	title := "Spotify Remote"
	artist := "No active Spotify device"
	albumName := "Start Spotify on phone or PC"
	progress := "0:00"
	duration := "0:00"
	volume := "?"
	shuffle := "?"
	repeat := "off"
	contextLabel := ""
	playIcon := "PLAY"
	coverPath := ""
	deviceName := ""
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			artist = "Session abgelaufen"
			albumName = "Bitte erneut einloggen"
		} else {
			artist = "Failed to get playback state"
			albumName = err.Error()
		}
	} else if code == http.StatusNoContent {
		artist = "No active Spotify device"
	} else {
		title = p.CurrentTrack.Name
		artist = artistNames(p.CurrentTrack.Artists)
		albumName = p.CurrentTrack.Album.Name
		progress = fmtMS(p.ProgressMS)
		duration = fmtMS(p.CurrentTrack.DurationMS)
		volume = strconv.Itoa(p.Device.VolumePercent)
		shuffle = strconv.FormatBool(p.Shuffle)
		repeat = p.Repeat
		deviceName = p.Device.Name
		contextLabel = a.playbackContextLabel(p)
		if contextLabel != "" {
			albumName = contextLabel
		}
		coverPath = a.prepareCover(p.CurrentTrack.Album.Images)
		if p.IsPlaying {
			playIcon = "PAUSE"
		}
	}

	a.fbinkClear()
	time.Sleep(250 * time.Millisecond)
	a.fbinkText(4, 1, "SPOTIFY REMOTE")
	if coverPath != "" {
		a.fbinkImage(coverPath)
	} else {
		a.fbinkText(4, 4, "+====================+")
		a.fbinkText(4, 5, "|                    |")
		a.fbinkText(4, 6, "|    ALBUM COVER     |")
		a.fbinkText(4, 7, "|                    |")
		a.fbinkText(4, 8, "+====================+")
	}
	a.fbinkText(4, 25, "====================")
	a.fbinkText(2, -4, "Refresh 8s. Quit only in lower-right.")
	a.fbinkText(6, 13, safe(title, 18))
	a.fbinkText(4, 18, safe(artist, 24))
	a.fbinkText(4, 22, safe(albumName, 24))
	a.fbinkText(4, 27, progress+"          "+duration)
	a.fbinkText(5, 31, "|<   "+playIcon+"   >|")
	a.fbinkText(4, 35, "VOL-  "+volume+"%  VOL+")
	a.fbinkText(3, 39, "SHUF "+shuffle+"  REP "+repeat)
	if deviceName != "" {
		a.fbinkText(3, 43, safe("DEV: "+deviceName, 35))
	}
	log.Printf("FBInk UI drawn: %s / %s / %s", title, artist, contextLabel)
}

// playbackContextLabel builds a Kindle-friendly label for the Spotify playback context.
// It handles collection, playlist, album, and artist contexts, optionally queries the context href for a name, and falls back to compact URI or URL references when text cannot render well.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) playbackContextLabel(p playback) string {
	ctx := p.Context
	if ctx.Type == "" && ctx.URI == "" && ctx.Href == "" && len(ctx.ExternalURLs) == 0 {
		return ""
	}
	if ctx.Type == "collection" {
		return "Liked Songs"
	}
	prefix := contextPrefix(ctx.Type)
	ref := contextRef(ctx)
	if ctx.Href != "" {
		if name := a.spotifyResourceName(ctx.Href); name != "" {
			return contextDisplay(prefix, name, ref)
		}
	}
	if ref != "" {
		return prefix + ": " + ref
	}
	if strings.EqualFold(ctx.Type, "playlist") && !a.hasTokenScopes("playlist-read-private", "playlist-read-collaborative") {
		return "Login for Playlist"
	}
	return prefix + ": unavailable"
}

// contextDisplay chooses the best visible context string for the Kindle display.
// It prefers names with ASCII letters or digits, falls back to a compact Spotify reference, and otherwise reports the context as unavailable.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func contextDisplay(prefix, name, fallback string) string {
	name = strings.TrimSpace(name)
	if kindleVisible(name) {
		return prefix + ": " + name
	}
	if fallback != "" {
		return prefix + ": " + fallback
	}
	return prefix + ": unavailable"
}

// kindleVisible reports whether text contains characters likely to survive Kindle eips rendering.
// It scans for ASCII letters or digits because some Kindle font paths render non-Latin or symbol-only names poorly.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func kindleVisible(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
		if r >= 'A' && r <= 'Z' {
			return true
		}
		if r >= 'a' && r <= 'z' {
			return true
		}
	}
	return false
}

// spotifyResourceName looks up the display name for a Spotify resource href.
// It sends an authorized GET to the supplied Spotify Web API endpoint and returns the trimmed name field, logging and suppressing lookup errors.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) spotifyResourceName(endpoint string) string {
	var out struct {
		Name string `json:"name"`
	}
	_, err := a.spotifyAPI(http.MethodGet, endpoint, nil, &out)
	if err != nil {
		log.Printf("context name lookup failed for %s: %v", endpoint, err)
		return ""
	}
	return strings.TrimSpace(out.Name)
}

// contextPrefix converts a Spotify context type into a display prefix.
// It normalizes known values and title-cases unknown non-empty values for concise Kindle labels.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func contextPrefix(contextType string) string {
	switch strings.ToLower(contextType) {
	case "playlist":
		return "Playlist"
	case "album":
		return "Album"
	case "artist":
		return "Artist"
	case "collection":
		return "Context"
	default:
		if contextType == "" {
			return "Context"
		}
		return strings.ToUpper(contextType[:1]) + contextType[1:]
	}
}

// shortSpotifyURI extracts the final identifier segment from a spotify:* URI.
// It splits on colons and returns the last part for fallback display.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func shortSpotifyURI(uri string) string {
	parts := strings.Split(uri, ":")
	if len(parts) == 0 {
		return uri
	}
	return parts[len(parts)-1]
}

// shortSpotifyRef extracts a compact identifier from a Spotify URI or URL.
// It handles spotify:* URIs, parses URL paths, and falls back to trimmed input when parsing fails.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func shortSpotifyRef(raw string) string {
	if strings.HasPrefix(raw, "spotify:") {
		return shortSpotifyURI(raw)
	}
	u, err := url.Parse(raw)
	if err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i] != "" {
				return parts[i]
			}
		}
	}
	return strings.TrimSpace(raw)
}

// contextRef chooses the best compact fallback reference from playback context metadata.
// It prefers Spotify URI, then API href, then the public external Spotify URL.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func contextRef(ctx playbackContext) string {
	if ctx.URI != "" {
		return shortSpotifyURI(ctx.URI)
	}
	if ctx.Href != "" {
		return shortSpotifyRef(ctx.Href)
	}
	if raw := strings.TrimSpace(ctx.ExternalURLs["spotify"]); raw != "" {
		return shortSpotifyRef(raw)
	}
	return ""
}

// hasTokenScopes reports whether the saved token includes every requested Spotify scope.
// It loads data/token.json, splits the granted scope string, and returns false when the token is missing or any required scope is absent.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) hasTokenScopes(required ...string) bool {
	tok, err := a.loadToken()
	if err != nil {
		return false
	}
	scopes := map[string]bool{}
	for _, s := range strings.Fields(tok.Scope) {
		scopes[s] = true
	}
	for _, s := range required {
		if !scopes[s] {
			return false
		}
	}
	return true
}

// fbinkPath locates a usable FBInk binary on common Kindle installation paths.
// It returns the first existing non-directory path or an empty string when FBInk is unavailable.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) fbinkPath() string {
	for _, p := range []string{"/mnt/us/libkh/bin/fbink", "/mnt/us/koreader/fbink", "/mnt/us/extensions/MRInstaller/bin/KHF/fbink"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// fbinkClear clears the FBInk framebuffer and eips text layer.
// It invokes FBInk full/keep clear modes when available, then runs eips -c as a compatibility fallback.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) fbinkClear() {
	if p := a.fbinkPath(); p != "" {
		_ = exec.Command(p, "-q", "-f", "-c").Run()
		_ = exec.Command(p, "-q", "-k").Run()
	}
	eipsClear()
}

// fbinkText writes one FBInk text line at a Kindle row.
// It shells out to FBInk with size, margin, and y-position arguments and ignores command failures so UI refresh can continue.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) fbinkText(size, row int, text string) {
	if p := a.fbinkPath(); p != "" {
		_ = exec.Command(p, "-q", "-S", strconv.Itoa(size), "-m", "-y", strconv.Itoa(row), text).Run()
	}
}

// fbinkImage renders album artwork through FBInk.
// It tries progressively simpler graphics arguments because FBInk builds differ in support for resize, dither, and flatten options.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) fbinkImage(path string) {
	if p := a.fbinkPath(); p != "" {
		attempts := [][]string{
			{"-q", "-g", fmt.Sprintf("file=%s,x=388,y=95,w=460,h=460,dither,flatten", path)},
			{"-q", "-g", fmt.Sprintf("file=%s,x=388,y=95,w=460,h=460,dither", path)},
			{"-q", "-g", fmt.Sprintf("file=%s,x=388,y=95", path)},
		}
		for i, args := range attempts {
			cmd := exec.Command(p, args...)
			out, err := cmd.CombinedOutput()
			if err == nil {
				log.Printf("fbink image render ok via attempt %d args=%v", i+1, args)
				return
			}
			log.Printf("fbink image render failed attempt %d args=%v err=%v out=%s", i+1, args, err, strings.TrimSpace(string(out)))
		}
	}
}

// prepareCover downloads and stores the best Spotify album image for FBInk rendering.
// It chooses a moderately sized image, downloads at most one megabyte, writes data/cover.jpg, logs failures, and returns an empty path when no usable cover is available.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) prepareCover(images []albumImage) string {
	if len(images) == 0 {
		return ""
	}
	best := images[0]
	for _, img := range images {
		if img.URL == "" {
			continue
		}
		if img.Width >= 280 && (best.Width < 280 || img.Width < best.Width) {
			best = img
		}
	}
	if best.URL == "" {
		best = images[len(images)-1]
	}
	if best.URL == "" {
		return ""
	}
	resp, err := a.client.Get(best.URL)
	if err != nil {
		log.Printf("cover download failed: %v", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("cover download bad status: %d", resp.StatusCode)
		return ""
	}
	coverPath := filepath.Join(a.base, "data", "cover.jpg")
	f, err := os.Create(coverPath)
	if err != nil {
		log.Printf("cover create failed: %v", err)
		return ""
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 1024*1024)); err != nil {
		_ = f.Close()
		log.Printf("cover write failed: %v", err)
		return ""
	}
	if err := f.Close(); err != nil {
		log.Printf("cover close failed: %v", err)
		return coverPath
	}
	log.Printf("cover prepared: %s", coverPath)
	return coverPath
}

// grabTouchLoop grabs candidate Linux input devices for exclusive fullscreen touch handling.
// It probes /dev/input/event0..5 for absolute touch calibration, EVIOCGRABs usable devices, and starts readers that emit UI actions until done closes.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) grabTouchLoop(out chan<- string, done <-chan struct{}) {
	var grabbed int
	for _, path := range []string{"/dev/input/event0", "/dev/input/event1", "/dev/input/event2", "/dev/input/event3", "/dev/input/event4", "/dev/input/event5"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		cal, ok := queryInputAbsCalibration(f)
		if !ok {
			_ = f.Close()
			continue
		}
		log.Printf("Trying touch grab on %s (%s x=%d..%d y=%d..%d)", path, cal.Source, cal.MinX, cal.MaxX, cal.MinY, cal.MaxY)
		if err := ioctlGrab(f, true); err != nil {
			log.Printf("Touch grab failed on %s: %v", path, err)
			_ = f.Close()
			continue
		}
		grabbed++
		log.Printf("Touch grabbed on %s", path)
		go a.readGrabbedTouch(f, out, done)
	}
	if grabbed == 0 {
		log.Printf("No touch device grabbed")
	} else {
		log.Printf("Touch grabbed on %d device(s)", grabbed)
	}
}

// readGrabbedTouch translates grabbed Linux input events into fullscreen UI actions.
// It tracks ABS coordinates, touch-key or multitouch release events, normalizes the final contact point, and releases EVIOCGRAB when the loop exits.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) readGrabbedTouch(f *os.File, out chan<- string, done <-chan struct{}) {
	defer f.Close()
	defer ioctlGrab(f, false)
	cal := a.touchCalibration(f, f.Name())
	var x, y int
	var hasCoords bool
	var touching bool
	var sawTouchKey bool
	var sawTrackingID bool
	var pendingRelease bool
	for {
		select {
		case <-done:
			return
		default:
		}
		var ev inputEvent
		if err := binary.Read(f, binary.LittleEndian, &ev); err != nil {
			log.Printf("Touch read failed: %v", err)
			return
		}
		switch ev.Type {
		case 3: // EV_ABS
			switch ev.Code {
			case 0x00, 0x35:
				x = int(ev.Value)
				hasCoords = true
			case 0x01, 0x36:
				y = int(ev.Value)
				hasCoords = true
			case 0x39:
				sawTrackingID = true
				if ev.Value >= 0 {
					touching = true
					pendingRelease = false
				} else {
					touching = false
					pendingRelease = true
				}
			}
		case 1: // EV_KEY
			switch ev.Code {
			case 0x14a, 0x145:
				sawTouchKey = true
				if ev.Value == 1 {
					touching = true
					pendingRelease = false
				} else if ev.Value == 0 {
					touching = false
					pendingRelease = true
				}
			}
		case 0: // EV_SYN
			if ev.Code != 0 {
				continue
			}
			if pendingRelease && hasCoords {
				a.queueUIAction(out, x, y, cal)
				hasCoords = false
				pendingRelease = false
				continue
			}
			if !touching && !sawTouchKey && !sawTrackingID && hasCoords {
				a.queueUIAction(out, x, y, cal)
				hasCoords = false
			}
		}
	}
}

// queueUIAction hit-tests a raw touch release against the FBInk fixed control zones.
// It normalizes raw coordinates with calibration, logs the hit result, and non-blockingly queues the action so touch reading never stalls.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) queueUIAction(out chan<- string, rawX, rawY int, cal touchCalibration) {
	nx, ny := a.normalizeTouch(rawX, rawY, cal)
	action := ""
	label := "empty"
	for _, zone := range a.fbinkTouchZones() {
		if nx >= zone.X1 && nx <= zone.X2 && ny >= zone.Y1 && ny <= zone.Y2 {
			action = zone.Action
			label = zone.Label
			break
		}
	}
	log.Printf("UI tap raw=(%d,%d) normalized=(%d,%d) calibration=%s hit=%s action=%s", rawX, rawY, nx, ny, cal.Source, label, action)
	if action == "" {
		return
	}
	select {
	case out <- action:
	default:
	}
}

// fbinkTouchZones returns the fixed framebuffer rectangles for the fullscreen FBInk layout.
// The coordinates match the 1236x1648 Kindle layout used by drawFBInkNowPlaying for volume, shuffle, repeat, transport, and quit controls.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) fbinkTouchZones() []uiTouchZone {
	return []uiTouchZone{
		{Action: "voldown", Label: "vol-down-mid", X1: 420, Y1: 1020, X2: 585, Y2: 1168},
		{Action: "volup", Label: "vol-up-mid", X1: 720, Y1: 1020, X2: 885, Y2: 1168},
		{Action: "shuffle", Label: "shuffle", X1: 390, Y1: 900, X2: 660, Y2: 1015},
		{Action: "repeat", Label: "repeat", X1: 705, Y1: 900, X2: 970, Y2: 1015},
		{Action: "prev", Label: "prev", X1: 0, Y1: 1190, X2: 420, Y2: 1590},
		{Action: "playpause", Label: "playpause", X1: 438, Y1: 1235, X2: 796, Y2: 1480},
		{Action: "next", Label: "next", X1: 820, Y1: 1210, X2: 1115, Y2: 1590},
		{Action: "quit", Label: "quit-corner", X1: 980, Y1: 1490, X2: 1236, Y2: 1648},
	}
}

// runKUAL dispatches a single KUAL menu action.
// It maps action names from extension menu items to login, status, playback, now-playing data, or recovery behavior and writes results for KUAL display.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) runKUAL(action string) {
	log.Printf("KUAL action: %s", action)
	switch action {
	case "login":
		a.kualLoginFile()
	case "finish-login":
		a.kualFinishLoginFile()
	case "browser-login":
		a.kualLogin()
	case "status":
		a.kualStatus()
	case "playpause":
		a.kualPlayPause()
	case "next":
		a.kualControl(http.MethodPost, "https://api.spotify.com/v1/me/player/next", nil, "Next")
	case "prev":
		a.kualControl(http.MethodPost, "https://api.spotify.com/v1/me/player/previous", nil, "Previous")
	case "voldown":
		a.kualVolume(-10)
	case "volup":
		a.kualVolume(10)
	case "shuffle":
		a.kualToggleShuffle()
	case "repeat":
		a.kualToggleRepeat()
	case "nowplaying":
		a.kualNowPlayingData()
	case "recover":
		eipsClear()
		eips(2, 0, "Spotify Remote")
		eips(4, 0, "Recovery done.")
		eips(6, 0, "Open KUAL again.")
	default:
		a.kualStatus()
	}
}

// kualNowPlayingData writes a machine-readable now-playing snapshot for KUAL helpers.
// It calls GET /v1/me/player, serializes success or error fields to data/nowplaying.json, and writes a short status message.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualNowPlayingData() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	out := map[string]any{
		"ok": false,
	}
	if err != nil {
		out["error"] = "Failed to get playback state: " + err.Error()
	} else if code == http.StatusNoContent {
		out["error"] = "No active Spotify device"
	} else {
		out["ok"] = true
		out["title"] = p.CurrentTrack.Name
		out["artist"] = artistNames(p.CurrentTrack.Artists)
		out["album"] = p.CurrentTrack.Album.Name
		out["is_playing"] = p.IsPlaying
		out["progress"] = fmtMS(p.ProgressMS)
		out["duration"] = fmtMS(p.CurrentTrack.DurationMS)
		out["volume"] = p.Device.VolumePercent
		out["shuffle"] = p.Shuffle
		out["repeat"] = p.Repeat
	}
	_ = writeJSON(filepath.Join(a.base, "data", "nowplaying.json"), out)
	if ok, _ := out["ok"].(bool); ok {
		a.kualPrint("Now Playing data updated", fmt.Sprint(out["title"]), fmt.Sprint(out["artist"]))
	} else {
		a.kualPrint("Now Playing failed", fmt.Sprint(out["error"]))
	}
}

// kualPrint writes user-facing KUAL status output.
// It prefixes lines with SPOTIFY REMOTE, logs each line, and writes data/status.txt for shell/menu scripts to display.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualPrint(lines ...string) {
	var out []string
	out = append(out, "SPOTIFY REMOTE")
	for i, line := range lines {
		_ = i
		out = append(out, line)
		log.Printf("KUAL: %s", line)
	}
	statusPath := filepath.Join(a.base, "data", "status.txt")
	_ = os.WriteFile(statusPath, []byte(strings.Join(out, "\n")+"\n"), 0644)
}

// kualStatus fetches and prints a compact Spotify playback status for KUAL.
// It handles expired sessions, missing devices, and successful now-playing details including progress, volume, shuffle, and repeat.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualStatus() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			a.kualPrint("Session abgelaufen", "Bitte erneut einloggen.", "Run Create Login URL.", "Then Finish Login From callback.txt.")
			return
		}
		a.kualPrint("ERROR", "Failed to get playback state:", err.Error(), "Use Login first.")
		return
	}
	if code == http.StatusNoContent {
		a.kualPrint("No active Spotify device", "Start Spotify on phone/PC.", "Then run Status again.")
		return
	}
	a.kualPrint(
		playText(p.IsPlaying)+"  "+fmtProgress(p.ProgressMS, p.CurrentTrack.DurationMS),
		p.CurrentTrack.Name,
		artistNames(p.CurrentTrack.Artists),
		"Vol "+strconv.Itoa(p.Device.VolumePercent)+"  Shuffle "+strconv.FormatBool(p.Shuffle),
		"Repeat "+p.Repeat,
	)
}

// kualLogin performs an interactive KUAL/browser PKCE login.
// It creates PKCE state, opens the Kindle browser to Spotify authorize, runs a temporary callback server for up to five minutes, exchanges the code, and prints the result.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualLogin() {
	if !validClientID(a.cfg.ClientID) {
		a.kualPrint("Spotify Client ID missing", "Edit data/config.json", "Use your own Client ID.", "Do not add a Client Secret.")
		return
	}
	// The PKCE code_verifier must be high entropy because Spotify later compares it with the S256 challenge.
	verifier := randomString(64)
	// The OAuth state value binds the callback to this login attempt and prevents accepting an unrelated redirect.
	state := randomString(24)
	// Spotify requires the S256 code_challenge, which is SHA-256(verifier) encoded with unpadded base64url.
	challenge := pkceChallenge(verifier)
	// data/oauth.json persists the verifier and state until the callback supplies the authorization code.
	if err := writeJSON(filepath.Join(a.base, "data", "oauth.json"), oauthState{State: state, CodeVerifier: verifier, CreatedAt: time.Now().Unix()}); err != nil {
		a.kualPrint("Login state error", err.Error())
		return
	}
	// url.Values performs the percent-encoding Spotify expects for the authorization or token request.
	v := url.Values{}
	// client_id identifies the public Spotify application registered for this redirect URI.
	v.Set("client_id", a.cfg.ClientID)
	// response_type=code starts the Authorization Code with PKCE flow rather than an implicit-token flow.
	v.Set("response_type", "code")
	// redirect_uri must exactly match the URI registered in the Spotify developer dashboard.
	v.Set("redirect_uri", a.cfg.Redirect)
	// code_challenge_method=S256 tells Spotify to verify the SHA-256 PKCE challenge.
	v.Set("code_challenge_method", "S256")
	// code_challenge is safe to send to Spotify because the secret verifier remains only in data/oauth.json.
	v.Set("code_challenge", challenge)
	// state is echoed by Spotify on redirect and checked before exchanging the authorization code.
	v.Set("state", state)
	// scope requests the playback and, in the native UI, playlist permissions needed by the remote.
	v.Set("scope", scopes)
	// The authorization URL sends the user to Spotify Accounts to approve the requested scopes.
	authURL := "https://accounts.spotify.com/authorize?" + v.Encode()
	a.kualPrint("Opening Spotify Login", "Waiting up to 5 minutes.", "Return to KUAL after login.")
	openBrowser(authURL)

	done := make(chan error, 1)
	srv := &http.Server{Addr: "127.0.0.1:" + strconv.Itoa(max(a.cfg.Port, 8787))}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if errText := r.URL.Query().Get("error"); errText != "" {
			done <- errors.New(errText)
			http.Error(w, errText, http.StatusBadRequest)
			return
		}
		err := a.exchangeCode(r.URL.Query().Get("code"), r.URL.Query().Get("state"))
		if err != nil {
			done <- err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "Spotify Remote login ok. Return to KUAL.")
		done <- nil
	})
	srv.Handler = mux
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			done <- err
		}
	}()
	select {
	case err := <-done:
		_ = srv.Close()
		if err != nil {
			a.kualPrint("Login failed", err.Error())
			return
		}
		a.kualPrint("Login ok", "Run Status next.")
	case <-time.After(5 * time.Minute):
		_ = srv.Close()
		a.kualPrint("Login timeout", "Run Login again.")
	}
}

// kualLoginFile creates an offline/manual KUAL login URL workflow.
// It writes data/login_url.txt and a callback.txt template so the user can authorize on another device and paste the redirect URL back onto the Kindle.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualLoginFile() {
	if !validClientID(a.cfg.ClientID) {
		a.kualPrint("Spotify Client ID missing", "Edit data/config.json", "Use your own Client ID.", "Do not add a Client Secret.")
		return
	}
	authURL, err := a.prepareAuthURL()
	if err != nil {
		a.kualPrint("Login setup failed", err.Error())
		return
	}
	path := filepath.Join(a.base, "data", "login_url.txt")
	helpPath := filepath.Join(a.base, "data", "callback.txt")
	if err := os.WriteFile(path, []byte(authURL+"\n"), 0644); err != nil {
		a.kualPrint("Cannot write login_url.txt", err.Error())
		return
	}
	_ = os.WriteFile(helpPath, []byte("Paste Spotify redirect URL or code here, then run Finish Login in KUAL.\n"), 0644)
	a.kualPrint(
		"Login URL written:",
		"data/login_url.txt",
		"Open it on PC/phone.",
		"Paste redirect URL into:",
		"data/callback.txt",
		"Then run Finish Login.",
	)
}

// kualFinishLoginFile completes manual login from data/callback.txt.
// It reads the pasted redirect URL or code, validates/parses it, exchanges it for tokens, updates callback.txt, and writes KUAL status.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualFinishLoginFile() {
	path := filepath.Join(a.base, "data", "callback.txt")
	raw, err := os.ReadFile(path)
	if err != nil {
		a.kualPrint("callback.txt missing", "Run Login first.", "Then edit data/callback.txt")
		return
	}
	code, state, err := parseCallbackValue(string(raw))
	if err != nil {
		a.kualPrint("Invalid callback.txt", err.Error())
		return
	}
	if err := a.exchangeCode(code, state); err != nil {
		a.kualPrint("Finish Login failed", err.Error())
		return
	}
	_ = os.WriteFile(path, []byte("Login complete. You may clear this file.\n"), 0644)
	a.kualPrint("Login complete", "Run Status next.")
}

// prepareAuthURL creates and persists a Spotify PKCE authorization URL.
// It generates the verifier, challenge, and state, writes data/oauth.json, and returns the encoded Spotify /authorize URL.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) prepareAuthURL() (string, error) {
	// The PKCE code_verifier must be high entropy because Spotify later compares it with the S256 challenge.
	verifier := randomString(64)
	// The OAuth state value binds the callback to this login attempt and prevents accepting an unrelated redirect.
	state := randomString(24)
	// Spotify requires the S256 code_challenge, which is SHA-256(verifier) encoded with unpadded base64url.
	challenge := pkceChallenge(verifier)
	// data/oauth.json persists the verifier and state until the callback supplies the authorization code.
	if err := writeJSON(filepath.Join(a.base, "data", "oauth.json"), oauthState{State: state, CodeVerifier: verifier, CreatedAt: time.Now().Unix()}); err != nil {
		return "", err
	}
	// url.Values performs the percent-encoding Spotify expects for the authorization or token request.
	v := url.Values{}
	// client_id identifies the public Spotify application registered for this redirect URI.
	v.Set("client_id", a.cfg.ClientID)
	// response_type=code starts the Authorization Code with PKCE flow rather than an implicit-token flow.
	v.Set("response_type", "code")
	// redirect_uri must exactly match the URI registered in the Spotify developer dashboard.
	v.Set("redirect_uri", a.cfg.Redirect)
	// code_challenge_method=S256 tells Spotify to verify the SHA-256 PKCE challenge.
	v.Set("code_challenge_method", "S256")
	// code_challenge is safe to send to Spotify because the secret verifier remains only in data/oauth.json.
	v.Set("code_challenge", challenge)
	// state is echoed by Spotify on redirect and checked before exchanging the authorization code.
	v.Set("state", state)
	// scope requests the playback and, in the native UI, playlist permissions needed by the remote.
	v.Set("scope", scopes)
	// The returned authorization URL is copied into the manual login file for use on another device.
	return "https://accounts.spotify.com/authorize?" + v.Encode(), nil
}

// parseCallbackValue extracts an OAuth code and optional state from manual callback text.
// It accepts a full redirect URL, a query string, or a raw code and rejects empty/template content.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func parseCallbackValue(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "Paste Spotify redirect") {
		return "", "", errors.New("Paste redirect URL or code into callback.txt")
	}
	lines := strings.Split(raw, "\n")
	raw = strings.TrimSpace(lines[0])
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", errors.New("Invalid redirect URL")
		}
		code := u.Query().Get("code")
		if code == "" {
			return "", "", errors.New("Redirect URL has no code")
		}
		return code, u.Query().Get("state"), nil
	}
	if strings.Contains(raw, "code=") {
		values, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
		if err == nil && values.Get("code") != "" {
			return values.Get("code"), values.Get("state"), nil
		}
	}
	return raw, "", nil
}

// kualPlayPause toggles Spotify playback for the active device.
// It reads current playback state, handles no-device and expired-session errors, and calls pause or play according to is_playing.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualPlayPause() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	if err != nil || code == http.StatusNoContent {
		if err == nil {
			err = errors.New("No active Spotify device")
		}
		if errors.Is(err, errSessionExpired) {
			a.kualPrint("Session abgelaufen", "Bitte erneut einloggen.", "Run Create Login URL.", "Then Finish Login From callback.txt.")
			return
		}
		a.kualPrint("Play/Pause failed", err.Error())
		return
	}
	if p.IsPlaying {
		a.kualControl(http.MethodPut, "https://api.spotify.com/v1/me/player/pause", nil, "Pause")
	} else {
		a.kualControl(http.MethodPut, "https://api.spotify.com/v1/me/player/play", nil, "Play")
	}
}

// kualControl sends one Spotify playback control and prints a KUAL result.
// It invokes spotifyAPI with the supplied method, endpoint, and optional JSON body, translating session expiry into login instructions.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualControl(method, endpoint string, body io.Reader, label string) {
	_, err := a.spotifyAPI(method, endpoint, body, nil)
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			a.kualPrint("Session abgelaufen", "Bitte erneut einloggen.", "Run Create Login URL.", "Then Finish Login From callback.txt.")
			return
		}
		a.kualPrint(label+" failed", err.Error())
		return
	}
	a.kualPrint(label+" sent", "Run Status to refresh.")
}

// kualVolume adjusts active Spotify device volume by a delta.
// It reads current device volume, clamps the new value to 0..100, and calls Spotify volume control with user-modify-playback-state scope.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualVolume(delta int) {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	if err != nil || code == http.StatusNoContent {
		if err == nil {
			err = errors.New("No active Spotify device")
		}
		if errors.Is(err, errSessionExpired) {
			a.kualPrint("Session abgelaufen", "Bitte erneut einloggen.", "Run Create Login URL.", "Then Finish Login From callback.txt.")
			return
		}
		a.kualPrint("Volume failed", err.Error())
		return
	}
	v := clamp(p.Device.VolumePercent+delta, 0, 100)
	a.kualControl(http.MethodPut, "https://api.spotify.com/v1/me/player/volume?volume_percent="+strconv.Itoa(v), nil, "Volume "+strconv.Itoa(v))
}

// kualToggleShuffle flips Spotify shuffle state.
// It reads the current shuffle flag and sends PUT /v1/me/player/shuffle with the opposite state.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualToggleShuffle() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	if err != nil || code == http.StatusNoContent {
		if err == nil {
			err = errors.New("No active Spotify device")
		}
		if errors.Is(err, errSessionExpired) {
			a.kualPrint("Session abgelaufen", "Bitte erneut einloggen.", "Run Create Login URL.", "Then Finish Login From callback.txt.")
			return
		}
		a.kualPrint("Shuffle failed", err.Error())
		return
	}
	a.kualControl(http.MethodPut, "https://api.spotify.com/v1/me/player/shuffle?state="+strconv.FormatBool(!p.Shuffle), nil, "Shuffle")
}

// kualToggleRepeat cycles Spotify repeat mode.
// It reads repeat_state and cycles off to context to track to off through PUT /v1/me/player/repeat.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) kualToggleRepeat() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	if err != nil || code == http.StatusNoContent {
		if err == nil {
			err = errors.New("No active Spotify device")
		}
		if errors.Is(err, errSessionExpired) {
			a.kualPrint("Session abgelaufen", "Bitte erneut einloggen.", "Run Create Login URL.", "Then Finish Login From callback.txt.")
			return
		}
		a.kualPrint("Repeat failed", err.Error())
		return
	}
	next := "context"
	if p.Repeat == "context" {
		next = "track"
	} else if p.Repeat == "track" {
		next = "off"
	}
	a.kualControl(http.MethodPut, "https://api.spotify.com/v1/me/player/repeat?state="+next, nil, "Repeat "+next)
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

// setupLog configures package logging for the native runtime.
// It creates logs/spotify-remote.log when possible and directs log output there with timestamps and short file names.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) setupLog() {
	p := filepath.Join(a.base, "logs", "spotify-remote.log")
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		log.SetOutput(f)
	}
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

// loadConfig reads and normalizes data/config.json.
// It starts from defaults, creates a template when the file is missing, applies fallback values, and returns read/write errors.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) loadConfig() error {
	a.cfg = defaultConfig()
	path := filepath.Join(a.base, "data", "config.json")
	err := readJSON(path, &a.cfg)
	if os.IsNotExist(err) {
		if writeErr := writeJSON(path, &a.cfg); writeErr != nil {
			return writeErr
		}
		log.Printf("created local config template: %s", path)
		return nil
	}
	a.normalizeConfig()
	return err
}

// defaultConfig returns conservative native Kindle defaults.
// It sets OAuth, screen, touch, eips, and button values that match the target Kindle layout until data/config.json overrides them.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func defaultConfig() config {
	return config{
		ClientID:          placeholderSpotifyClientID,
		Redirect:          "http://127.0.0.1:8787/callback",
		Port:              8787,
		RefreshSec:        8,
		ScreenWidth:       1236,
		ScreenHeight:      1648,
		TouchMinX:         0,
		TouchMaxX:         4095,
		TouchMinY:         0,
		TouchMaxY:         4095,
		TouchUseKernelAbs: boolPtr(true),
		EipsColWidth:      22,
		EipsRowHeight:     40,
		ButtonTop:         660,
		ButtonHeight:      88,
		ButtonGap:         2,
	}
}

// normalizeConfig repairs zero or invalid config values after loading JSON.
// It fills missing redirect, port, refresh, screen, touch, eips, and button settings without overwriting intentional non-zero values.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) normalizeConfig() {
	if a.cfg.Redirect == "" {
		a.cfg.Redirect = "http://127.0.0.1:8787/callback"
	}
	if a.cfg.Port == 0 {
		a.cfg.Port = 8787
	}
	if a.cfg.RefreshSec == 0 {
		a.cfg.RefreshSec = 8
	}
	if a.cfg.ScreenWidth == 0 {
		a.cfg.ScreenWidth = 1236
	}
	if a.cfg.ScreenHeight == 0 {
		a.cfg.ScreenHeight = 1648
	}
	if a.cfg.TouchMaxX <= a.cfg.TouchMinX {
		a.cfg.TouchMinX = 0
		a.cfg.TouchMaxX = 4095
	}
	if a.cfg.TouchMaxY <= a.cfg.TouchMinY {
		a.cfg.TouchMinY = 0
		a.cfg.TouchMaxY = 4095
	}
	if a.cfg.EipsColWidth == 0 {
		a.cfg.EipsColWidth = 22
	}
	if a.cfg.EipsRowHeight == 0 {
		a.cfg.EipsRowHeight = 40
	}
	if a.cfg.ButtonTop == 0 {
		a.cfg.ButtonTop = 660
	}
	if a.cfg.ButtonHeight == 0 {
		a.cfg.ButtonHeight = 88
	}
	if a.cfg.ButtonGap == 0 {
		a.cfg.ButtonGap = 2
	}
}

// boolPtr returns a pointer to a bool literal for config defaults.
// It is used where a nil pointer has semantic meaning distinct from false.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func boolPtr(v bool) *bool {
	return &v
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
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}

// refreshLoop polls Spotify playback state until the app quits.
// It ticks at the configured refresh interval with an eight-second floor, refreshes immediately, and exits when quit is signaled.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) refreshLoop() {
	t := time.NewTicker(time.Duration(max(a.cfg.RefreshSec, 8)) * time.Second)
	defer t.Stop()
	a.refresh()
	for {
		select {
		case <-a.quit:
			return
		case <-t.C:
			a.refresh()
		}
	}
}

// refresh fetches playback state and redraws the eips fallback UI.
// It calls GET /v1/me/player, updates protected app state for success, no-device, or error cases, and renders while holding the UI lock.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) refresh() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			a.status = "Session abgelaufen - bitte erneut einloggen"
			a.err = "Create Login URL -> Finish Login From callback.txt"
		} else {
			a.err = "Failed to get playback state: " + err.Error()
		}
		a.hasState = false
	} else if code == http.StatusNoContent {
		a.err = "No active Spotify device"
		a.hasState = false
	} else {
		a.state = p
		a.hasState = true
		a.err = ""
		a.status = "Updated " + time.Now().Format("15:04:05")
	}
	a.drawLocked()
}

// control handles one eips fallback UI action.
// It maps action strings to Spotify endpoints or local views, sends the selected request, updates status/error state, redraws, waits briefly, and refreshes playback.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) control(action string) {
	endpoint := ""
	method := http.MethodPut
	var body io.Reader
	a.mu.Lock()
	p := a.state
	a.mu.Unlock()
	switch action {
	case "playpause":
		if p.IsPlaying {
			endpoint = "https://api.spotify.com/v1/me/player/pause"
		} else {
			endpoint = "https://api.spotify.com/v1/me/player/play"
		}
	case "next":
		method = http.MethodPost
		endpoint = "https://api.spotify.com/v1/me/player/next"
	case "prev":
		method = http.MethodPost
		endpoint = "https://api.spotify.com/v1/me/player/previous"
	case "voldown":
		v := clamp(p.Device.VolumePercent-10, 0, 100)
		endpoint = "https://api.spotify.com/v1/me/player/volume?volume_percent=" + strconv.Itoa(v)
	case "volup":
		v := clamp(p.Device.VolumePercent+10, 0, 100)
		endpoint = "https://api.spotify.com/v1/me/player/volume?volume_percent=" + strconv.Itoa(v)
	case "shuffle":
		endpoint = "https://api.spotify.com/v1/me/player/shuffle?state=" + strconv.FormatBool(!p.Shuffle)
	case "repeat":
		next := "context"
		if p.Repeat == "context" {
			next = "track"
		} else if p.Repeat == "track" {
			next = "off"
		}
		endpoint = "https://api.spotify.com/v1/me/player/repeat?state=" + next
	case "devices":
		a.showDevices()
		return
	case "login":
		a.login()
		return
	case "quit":
		close(a.quit)
		return
	default:
		return
	}
	_, err := a.spotifyAPI(method, endpoint, body, nil)
	a.mu.Lock()
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			a.status = "Session abgelaufen - bitte erneut einloggen"
			a.err = "Create Login URL -> Finish Login From callback.txt"
			a.hasState = false
		} else {
			a.err = err.Error()
		}
	} else {
		a.status = action + " sent"
		a.err = ""
	}
	a.drawLocked()
	a.mu.Unlock()
	time.Sleep(900 * time.Millisecond)
	a.refresh()
}

// showDevices displays available Spotify playback devices and installs device-transfer buttons.
// It calls GET /v1/me/player/devices, draws the device list with eips, and assigns touch callbacks that PUT /v1/me/player to transfer playback.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) showDevices() {
	var out struct {
		Devices []device `json:"devices"`
	}
	_, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player/devices", nil, &out)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			a.status = "Session abgelaufen - bitte erneut einloggen"
			a.err = "Create Login URL -> Finish Login From callback.txt"
			a.hasState = false
		} else {
			a.err = err.Error()
		}
		a.drawLocked()
		return
	}
	eipsClear()
	eips(0, 0, "Spotify Remote - Devices")
	eips(2, 0, "Tap a device row. Bottom-right quits.")
	a.buttons = nil
	for i, d := range out.Devices {
		row := 4 + i*2
		label := d.Name + " (" + d.Type + ")"
		if d.IsActive {
			label = "* " + label
		}
		devID := d.ID
		eips(row, 0, safe(label, 55))
		a.buttons = append(a.buttons, uiButton{Label: label, X1: 0, Y1: a.rowToY(row), X2: a.cfg.ScreenWidth, Y2: a.rowToY(row + 2), Do: func() {
			body := strings.NewReader(fmt.Sprintf(`{"device_ids":["%s"],"play":false}`, devID))
			_, err := a.spotifyAPI(http.MethodPut, "https://api.spotify.com/v1/me/player", body, nil)
			a.mu.Lock()
			if err != nil {
				if errors.Is(err, errSessionExpired) {
					a.status = "Session abgelaufen - bitte erneut einloggen"
					a.err = "Create Login URL -> Finish Login From callback.txt"
					a.hasState = false
				} else {
					a.err = err.Error()
				}
			} else {
				a.status = "Device selected"
			}
			a.mu.Unlock()
			a.refresh()
		}})
	}
	a.buttons = append(a.buttons, uiButton{Label: "Back", X1: a.cfg.ScreenWidth * 7 / 10, Y1: a.cfg.ScreenHeight * 7 / 8, X2: a.cfg.ScreenWidth, Y2: a.cfg.ScreenHeight, Do: func() { a.refresh() }})
	eips(38, 40, "Back")
}

// login starts the eips fallback PKCE login flow.
// It validates the client ID, generates PKCE verifier/challenge/state, writes oauth.json, builds the Spotify authorize URL, and opens the Kindle browser.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) login() {
	a.mu.Lock()
	a.status = "Opening Spotify login"
	a.err = ""
	a.drawLocked()
	a.mu.Unlock()
	if !validClientID(a.cfg.ClientID) {
		a.mu.Lock()
		a.err = "Spotify Client ID missing"
		a.drawLocked()
		a.mu.Unlock()
		return
	}
	// The PKCE code_verifier must be high entropy because Spotify later compares it with the S256 challenge.
	verifier := randomString(64)
	// The OAuth state value binds the callback to this login attempt and prevents accepting an unrelated redirect.
	state := randomString(24)
	// Spotify requires the S256 code_challenge, which is SHA-256(verifier) encoded with unpadded base64url.
	challenge := pkceChallenge(verifier)
	// data/oauth.json persists the verifier and state until the callback supplies the authorization code.
	if err := writeJSON(filepath.Join(a.base, "data", "oauth.json"), oauthState{State: state, CodeVerifier: verifier, CreatedAt: time.Now().Unix()}); err != nil {
		a.mu.Lock()
		a.err = err.Error()
		a.drawLocked()
		a.mu.Unlock()
		return
	}
	// url.Values performs the percent-encoding Spotify expects for the authorization or token request.
	v := url.Values{}
	// client_id identifies the public Spotify application registered for this redirect URI.
	v.Set("client_id", a.cfg.ClientID)
	// response_type=code starts the Authorization Code with PKCE flow rather than an implicit-token flow.
	v.Set("response_type", "code")
	// redirect_uri must exactly match the URI registered in the Spotify developer dashboard.
	v.Set("redirect_uri", a.cfg.Redirect)
	// code_challenge_method=S256 tells Spotify to verify the SHA-256 PKCE challenge.
	v.Set("code_challenge_method", "S256")
	// code_challenge is safe to send to Spotify because the secret verifier remains only in data/oauth.json.
	v.Set("code_challenge", challenge)
	// state is echoed by Spotify on redirect and checked before exchanging the authorization code.
	v.Set("state", state)
	// scope requests the playback and, in the native UI, playlist permissions needed by the remote.
	v.Set("scope", scopes)
	// The authorization URL sends the user to Spotify Accounts to approve the requested scopes.
	authURL := "https://accounts.spotify.com/authorize?" + v.Encode()
	log.Printf("auth url: %s", authURL)
	openBrowser(authURL)
}

// callbackServer serves the local OAuth callback endpoint for the eips fallback UI.
// It listens on 127.0.0.1, handles Spotify error or code callbacks, exchanges valid codes, updates UI state, and starts a refresh after login.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) callbackServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		errText := r.URL.Query().Get("error")
		if errText != "" {
			http.Error(w, errText, http.StatusBadRequest)
			a.setError(errText)
			return
		}
		if err := a.exchangeCode(r.URL.Query().Get("code"), r.URL.Query().Get("state")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			a.setError(err.Error())
			return
		}
		_, _ = io.WriteString(w, "Login ok. Return to Spotify Remote.")
		a.mu.Lock()
		a.status = "Login ok"
		a.err = ""
		a.drawLocked()
		a.mu.Unlock()
		go a.refresh()
	})
	addr := "127.0.0.1:" + strconv.Itoa(max(a.cfg.Port, 8787))
	log.Printf("callback server on %s", addr)
	_ = http.ListenAndServe(addr, mux)
}

// setError records and displays a UI error.
// It updates protected app state and redraws the eips fallback screen.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) setError(msg string) {
	a.mu.Lock()
	a.err = msg
	a.drawLocked()
	a.mu.Unlock()
}

// exchangeCode exchanges a Spotify authorization code for tokens.
// It validates the saved PKCE state, posts authorization_code data to /api/token, stamps AuthorizedAt and ExpiresAt, and writes data/token.json.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) exchangeCode(code, state string) error {
	if code == "" {
		return errors.New("missing authorization code")
	}
	var st oauthState
	// The token exchange must reload the original PKCE verifier and state from data/oauth.json.
	if err := readJSON(filepath.Join(a.base, "data", "oauth.json"), &st); err != nil {
		return errors.New("login state missing; tap Login again")
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
	form.Set("redirect_uri", a.cfg.Redirect)
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
	return writeJSON(filepath.Join(a.base, "data", "token.json"), &tok)
}

// loadToken returns a valid Spotify access token, refreshing it when needed.
// It reads data/token.json, checks ExpiresAt, posts refresh_token to /api/token when stale, preserves refresh token/scope/AuthorizedAt, clears invalid_grant sessions, and rewrites token.json.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) loadToken() (tokenFile, error) {
	var tok tokenFile
	// loadToken reads data/token.json before every Spotify API call so controls survive process restarts.
	if err := readJSON(filepath.Join(a.base, "data", "token.json"), &tok); err != nil || tok.AccessToken == "" {
		return tok, errors.New("Token missing; tap Login")
	}
	// A future ExpiresAt means the bearer token can be reused without contacting Spotify Accounts.
	if time.Now().Unix() < tok.ExpiresAt {
		return tok, nil
	}
	if tok.RefreshToken == "" {
		return tok, errors.New("Token expired; tap Login")
	}
	form := url.Values{}
	// Spotify token requests for public PKCE clients include client_id but no client secret.
	form.Set("client_id", a.cfg.ClientID)
	// grant_type=refresh_token asks Spotify Accounts for a new access token using the saved refresh token.
	form.Set("grant_type", "refresh_token")
	// refresh_token is the long-lived credential stored in data/token.json.
	form.Set("refresh_token", tok.RefreshToken)
	var refreshed tokenFile
	if err := a.spotifyForm(spotifyTokenEndpoint, form, "", &refreshed); err != nil {
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
	refreshed.ExpiresAt = time.Now().Unix() + int64(refreshed.ExpiresIn) - 60
	// The refreshed token replaces data/token.json so subsequent API calls use the new access token.
	if err := writeJSON(filepath.Join(a.base, "data", "token.json"), &refreshed); err != nil {
		return tok, err
	}
	return refreshed, nil
}

// clearToken deletes the persisted Spotify token file.
// It removes data/token.json and treats an already-missing file as success.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) clearToken() error {
	// clearToken deletes data/token.json after invalid_grant or explicit session cleanup.
	err := os.Remove(filepath.Join(a.base, "data", "token.json"))
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return err
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
// Parameters: method, endpoint, body, and out match spotifyAPI; allowSchedule controls whether a 429 starts scheduleRetry or playback buffering.
// Return values: the Spotify HTTP status and an error, including errRateLimited for HTTP 429.
// Error conditions: token load, request creation, network, JSON decoding, and mapped Spotify HTTP errors are returned to callers.
// Side effects: can perform Spotify HTTP calls, read/write token files through loadToken, mutate rate-limit state, and schedule retry goroutines.
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
		activePlayback := a.isActivePlayback()
		// The Kindle display shows a countdown in the status area, or a secondary row during active playback so the main playback text remains visible.
		a.showRateLimitCountdown(retryAfter, activePlayback)
		retryFn := func() error {
			_, retryErr := a.spotifyAPIWithRetry(method, endpoint, bytes.NewReader(payload), out, false)
			return retryErr
		}
		if allowSchedule {
			if activePlayback {
				// Active Spotify playback is treated as a protected countdown period; buffer the failed call instead of interrupting playback UI state.
				a.bufferPendingPlaybackCall(retryFn, time.Duration(retryAfter)*time.Second)
			} else {
				// The first idle 429 outside OAuth starts one deferred retry; scheduled retries call this helper with allowSchedule=false.
				scheduleRetry(retryFn, time.Duration(retryAfter)*time.Second)
			}
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
	a.restoreRateLimitStatus()
	clearRateLimit()
	return resp.StatusCode, nil
}

// isActivePlayback reports whether the native UI currently has active Spotify playback.
// It snapshots app state under lock and treats hasState && state.IsPlaying as the active period that must not be interrupted by 429 retry handling.
// Parameters: none.
// Return values: true when a known playback state is actively playing, otherwise false.
// Error conditions: none.
// Side effects: reads app state protected by app.mu.
func (a *app) isActivePlayback() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hasState && a.state.IsPlaying
}

// bufferPendingPlaybackCall stores one failed Spotify call for replay during active playback.
// It replaces any older pending call, waits retryAfter in a goroutine, replays only if playback is still active, and logs a discard when playback has ended.
// Parameters: fn recreates the failed Spotify call; retryAfter is the Spotify Retry-After wait.
// Return values: none.
// Error conditions: replay errors are logged; a second errRateLimited marks the scheduled retry as non-retryable.
// Side effects: mutates pending playback state, starts one goroutine, may invoke Spotify once, and may clear rate-limit state.
func (a *app) bufferPendingPlaybackCall(fn func() error, retryAfter time.Duration) {
	rateLimitMu.Lock()
	pendingPlaybackCallID++
	callID := pendingPlaybackCallID
	// Only one playback call is buffered; newer 429s replace the previous pending function before its wait expires.
	pendingPlaybackCall = fn
	rateLimitMu.Unlock()
	go func() {
		// This goroutine waits out Retry-After before deciding whether the protected playback period is still active.
		time.Sleep(retryAfter)
		rateLimitMu.Lock()
		pending := pendingPlaybackCall
		currentID := pendingPlaybackCallID
		if callID != currentID {
			rateLimitMu.Unlock()
			log.Printf("discarded pending playback retry: replaced by newer rate-limited call")
			return
		}
		pendingPlaybackCall = nil
		rateLimitMu.Unlock()
		if !a.isActivePlayback() {
			log.Printf("discarded pending playback retry: playback ended before Retry-After elapsed")
			return
		}
		if pending == nil {
			return
		}
		err := pending()
		if errors.Is(err, errRateLimited) {
			rateLimitMu.Lock()
			// A second 429 from a replayed playback call is non-retryable; no further playback retry is buffered.
			rateLimitNonRetryable = true
			rateLimitMu.Unlock()
			return
		}
		if err != nil {
			log.Printf("pending playback retry failed: %v", err)
			return
		}
		clearRateLimit()
	}()
}

// showRateLimitCountdown displays and updates the native rate-limit wait message.
// It snapshots the previous status when idle, starts a countdown goroutine, and updates only the eips status row or FBInk secondary row once per second.
// Parameters: retryAfterSeconds is the parsed Spotify wait; activePlayback selects the non-blocking secondary playback area.
// Return values: none.
// Error conditions: display command failures are ignored by eips/fbinkText helpers.
// Side effects: starts one goroutine, mutates display bookkeeping, and writes partial status text to the Kindle display.
func (a *app) showRateLimitCountdown(retryAfterSeconds int, activePlayback bool) {
	rateLimitMu.Lock()
	rateLimitDisplayID++
	displayID := rateLimitDisplayID
	rateLimitMu.Unlock()
	a.mu.Lock()
	if !activePlayback && !a.rateLimitStatusActive {
		a.rateLimitPreviousStatus = a.status
		a.rateLimitStatusActive = true
	}
	a.mu.Unlock()
	go func() {
		// This goroutine updates only the status region once per second to avoid a full E-Ink refresh during the wait.
		for remaining := retryAfterSeconds; remaining > 0; remaining-- {
			rateLimitMu.Lock()
			currentID := rateLimitDisplayID
			rateLimitMu.Unlock()
			if displayID != currentID {
				return
			}
			a.writeRateLimitStatus(remaining, activePlayback)
			time.Sleep(time.Second)
		}
	}()
}

// writeRateLimitStatus writes one rate-limit countdown value to the native display.
// It uses row-only eips updates for the fallback UI and a secondary FBInk row during active playback so the main countdown/progress area is not obscured.
// Parameters: remaining is the seconds left; activePlayback selects status row versus secondary playback row.
// Return values: none.
// Error conditions: display command failures are ignored by eips/fbinkText helpers.
// Side effects: updates the Kindle display without clearing or redrawing the full screen.
func (a *app) writeRateLimitStatus(remaining int, activePlayback bool) {
	msg := fmt.Sprintf("Rate limited - retrying in %ds", remaining)
	if activePlayback {
		// During active playback the secondary lower row is updated so main playback progress remains visible.
		eips(8, 0, safe(msg, 55))
		a.fbinkText(2, 47, safe(msg, 35))
		return
	}
	a.mu.Lock()
	a.status = msg
	a.mu.Unlock()
	// The eips status row is updated directly to avoid drawLocked's full-screen clear and refresh.
	eips(3, 0, safe("Status: "+msg, 55))
}

// restoreRateLimitStatus restores the native status area after rate-limit recovery.
// It cancels older countdown goroutines, restores the previous idle status, and clears the secondary playback row used during active playback.
// Parameters: none.
// Return values: none.
// Error conditions: display command failures are ignored by eips/fbinkText helpers.
// Side effects: mutates display bookkeeping and writes row-only display updates.
func (a *app) restoreRateLimitStatus() {
	rateLimitMu.Lock()
	rateLimitDisplayID++
	rateLimitMu.Unlock()
	a.mu.Lock()
	previous := a.rateLimitPreviousStatus
	wasActive := a.rateLimitStatusActive
	if wasActive {
		// The prior status line is restored after a successful retry clears the current rate-limit wait.
		a.status = previous
		a.rateLimitPreviousStatus = ""
		a.rateLimitStatusActive = false
	}
	a.mu.Unlock()
	if wasActive {
		eips(3, 0, safe("Status: "+previous, 55))
	}
	// The secondary playback row is blanked after recovery so stale countdown text does not remain over the now-playing screen.
	eips(8, 0, strings.Repeat(" ", 55))
	a.fbinkText(2, 47, strings.Repeat(" ", 35))
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
	// The package-level rate-limit state drives retry scheduling and Kindle status countdown messages.
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
	return fmt.Errorf("Spotify API error HTTP %d: %.140s", status, text)
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

// draw redraws the eips fallback UI under lock.
// It acquires the app mutex and delegates to drawLocked.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) draw() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.drawLocked()
}

// drawLocked renders the complete eips fallback interface.
// It throttles rapid redraws, clears the screen, writes status, playback, tap diagnostics, and button rows, and rebuilds the touch button table.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) drawLocked() {
	if time.Since(a.lastDraw) < 200*time.Millisecond {
		return
	}
	a.lastDraw = time.Now()
	eipsClear()
	eips(1, 0, "SPOTIFY REMOTE")
	eips(3, 0, safe("Status: "+a.status, 55))
	if a.err != "" {
		eips(5, 0, "ERROR:")
		eips(6, 0, safe(a.err, 55))
	}
	row := 9
	if a.hasState {
		eips(row, 0, safe(a.state.CurrentTrack.Name, 55))
		eips(row+1, 0, safe(artistNames(a.state.CurrentTrack.Artists), 55))
		eips(row+2, 0, safe(a.state.CurrentTrack.Album.Name, 55))
		eips(row+4, 0, fmt.Sprintf("%s  %s  Vol %d", playText(a.state.IsPlaying), fmtProgress(a.state.ProgressMS, a.state.CurrentTrack.DurationMS), a.state.Device.VolumePercent))
		eips(row+5, 0, fmt.Sprintf("Shuffle %v  Repeat %s", a.state.Shuffle, a.state.Repeat))
		eips(row+6, 0, safe("Device: "+a.state.Device.Name, 55))
	} else {
		eips(row, 0, "No playback loaded.")
		eips(row+1, 0, "Tap LOGIN first.")
	}
	if a.lastTap != "" {
		eips(row+7, 0, safe(a.lastTap, 55))
	}
	a.buttons = []uiButton{
		a.button(0, "PREVIOUS", func() { go a.control("prev") }),
		a.button(1, "PLAY / PAUSE", func() { go a.control("playpause") }),
		a.button(2, "NEXT", func() { go a.control("next") }),
		a.button(3, "VOLUME -", func() { go a.control("voldown") }),
		a.button(4, "VOLUME +", func() { go a.control("volup") }),
		a.button(5, "SHUFFLE", func() { go a.control("shuffle") }),
		a.button(6, "REPEAT", func() { go a.control("repeat") }),
		a.button(7, "DEVICES", func() { go a.control("devices") }),
		a.button(8, "REFRESH", func() { go a.refresh() }),
		a.button(9, "LOGIN", func() { go a.control("login") }),
		a.button(10, "QUIT", func() { go a.control("quit") }),
	}
	for i, b := range a.buttons {
		r := a.yToRow(b.Y1)
		eips(r, 0, "--------------------------------------------------------")
		eips(r+1, 2, fmt.Sprintf("%02d  %s", i+1, b.Label))
	}
}

// button constructs one full-width eips fallback touch button.
// It converts a button slot into framebuffer coordinates using configured top, height, and gap values.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) button(slot int, label string, do func()) uiButton {
	step := a.cfg.ButtonHeight + a.cfg.ButtonGap
	y1 := a.cfg.ButtonTop + slot*step
	return uiButton{Label: label, X1: 0, Y1: y1, X2: a.cfg.ScreenWidth, Y2: y1 + a.cfg.ButtonHeight, Do: do}
}

// eipsClear clears the Kindle eips text display.
// It shells out to eips -c and ignores errors because some modes may not have eips available.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func eipsClear() {
	_ = exec.Command("eips", "-c").Run()
}

// eips writes one text string at an eips row and column.
// It substitutes a space for empty text because eips needs an argument to update a cell.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func eips(row, col int, text string) {
	if text == "" {
		text = " "
	}
	_ = exec.Command("eips", strconv.Itoa(row), strconv.Itoa(col), text).Run()
}

// openBrowser asks the Kindle application manager to open the browser.
// It sends a lipc-set-prop command with an app://com.lab126.browser URL containing the OAuth authorization URL.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func openBrowser(raw string) {
	_ = exec.Command("lipc-set-prop", "com.lab126.appmgrd", "start", "app://com.lab126.browser?url="+raw).Run()
}

// inputEvent mirrors Linux struct input_event as emitted by /dev/input/event* devices.
type inputEvent struct {
	Sec   int32  // Sec is the event timestamp seconds field supplied by the kernel and ignored by this UI.
	Usec  int32  // Usec is the event timestamp microseconds field supplied by the kernel and ignored by this UI.
	Type  uint16 // Type is the Linux input event class, such as EV_ABS, EV_KEY, or EV_SYN.
	Code  uint16 // Code is the event-specific identifier, such as ABS_X, ABS_MT_POSITION_X, or BTN_TOUCH.
	Value int32  // Value is the raw coordinate, key state, tracking ID, or sync value associated with Type and Code.
}

// touchCalibration stores raw-to-screen mapping metadata for a Kindle touch device.
type touchCalibration struct {
	MinX   int    // MinX is the smallest raw X value expected from the touch controller.
	MaxX   int    // MaxX is the largest raw X value expected from the touch controller.
	MinY   int    // MinY is the smallest raw Y value expected from the touch controller.
	MaxY   int    // MaxY is the largest raw Y value expected from the touch controller.
	Source string // Source identifies whether calibration came from config or kernel EVIOCGABS metadata.
}

// touchLoop discovers Linux input devices for the eips fallback UI.
// It scans /dev/input/event* periodically, starts one reader per newly discovered device, and keeps running for the process lifetime.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) touchLoop() {
	opened := make(map[string]bool)
	for {
		for i := 0; i < 12; i++ {
			p := "/dev/input/event" + strconv.Itoa(i)
			if !opened[p] {
				if _, err := os.Stat(p); err == nil {
					opened[p] = true
					log.Printf("opening input device %s", p)
					go a.readInput(p)
				}
			}
		}
		time.Sleep(30 * time.Second)
	}
}

// readInput reads raw Linux input events and converts touch releases into taps.
// It tracks ABS_X/ABS_Y or multitouch coordinates, touch key/tracking release semantics, and calls tap after SYN_REPORT finalizes an event frame.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) readInput(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	cal := a.touchCalibration(f, path)
	var x, y int
	var hasCoords bool
	var touching bool
	var sawTouchKey bool
	var sawTrackingID bool
	var pendingRelease bool
	for {
		var ev inputEvent
		if err := binary.Read(f, binary.LittleEndian, &ev); err != nil {
			return
		}
		switch ev.Type {
		case 3: // EV_ABS
			switch ev.Code {
			case 0x00, 0x35: // ABS_X, ABS_MT_POSITION_X
				x = int(ev.Value)
				hasCoords = true
			case 0x01, 0x36: // ABS_Y, ABS_MT_POSITION_Y
				y = int(ev.Value)
				hasCoords = true
			case 0x39: // ABS_MT_TRACKING_ID
				sawTrackingID = true
				if ev.Value >= 0 {
					touching = true
					pendingRelease = false
				} else {
					touching = false
					pendingRelease = true
				}
			}
		case 1: // EV_KEY
			switch ev.Code {
			case 0x14a, 0x145: // BTN_TOUCH, BTN_TOOL_FINGER
				sawTouchKey = true
				if ev.Value == 1 {
					touching = true
					pendingRelease = false
				} else if ev.Value == 0 {
					touching = false
					pendingRelease = true
				}
			}
		case 0: // EV_SYN (SYN_REPORT)
			if ev.Code != 0 {
				continue
			}
			if pendingRelease && hasCoords {
				a.tap(x, y, cal)
				hasCoords = false
				pendingRelease = false
				continue
			}
			// Last-resort fallback for unusual devices that emit only ABS+SYN.
			if !touching && !sawTouchKey && !sawTrackingID && hasCoords {
				a.tap(x, y, cal)
				hasCoords = false
			}
		}
	}
}

// touchCalibration chooses raw touch calibration ranges for an input device.
// It starts with config ranges, optionally queries kernel EVIOCGABS metadata, logs the selected source, and falls back to config on failure.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) touchCalibration(f *os.File, path string) touchCalibration {
	cal := touchCalibration{
		MinX:   a.cfg.TouchMinX,
		MaxX:   a.cfg.TouchMaxX,
		MinY:   a.cfg.TouchMinY,
		MaxY:   a.cfg.TouchMaxY,
		Source: "config",
	}
	if a.cfg.TouchUseKernelAbs != nil && !*a.cfg.TouchUseKernelAbs {
		log.Printf("touch calibration for %s: config x=%d..%d y=%d..%d", path, cal.MinX, cal.MaxX, cal.MinY, cal.MaxY)
		return cal
	}
	if kernelCal, ok := queryInputAbsCalibration(f); ok {
		log.Printf("touch calibration for %s: %s x=%d..%d y=%d..%d", path, kernelCal.Source, kernelCal.MinX, kernelCal.MaxX, kernelCal.MinY, kernelCal.MaxY)
		return kernelCal
	}
	log.Printf("touch calibration for %s: config fallback x=%d..%d y=%d..%d", path, cal.MinX, cal.MaxX, cal.MinY, cal.MaxY)
	return cal
}

// tap debounces and dispatches a normalized touch coordinate.
// It scales raw coordinates to screen pixels, snapshots current buttons under lock, invokes the hit button callback, and records hit/miss diagnostics.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) tap(rawX, rawY int, cal touchCalibration) {
	if time.Since(a.lastAction) < 500*time.Millisecond {
		return
	}
	a.lastAction = time.Now()
	x, y := a.normalizeTouch(rawX, rawY, cal)
	log.Printf("tap raw=(%d,%d) normalized=(%d,%d) calibration=%s x=%d..%d y=%d..%d", rawX, rawY, x, y, cal.Source, cal.MinX, cal.MaxX, cal.MinY, cal.MaxY)
	a.mu.Lock()
	buttons := append([]uiButton(nil), a.buttons...)
	a.mu.Unlock()
	for _, b := range buttons {
		if x >= b.X1 && x <= b.X2 && y >= b.Y1 && y <= b.Y2 {
			log.Printf("tap hit %s", b.Label)
			a.setLastTap(fmt.Sprintf("Tap %s raw=%d,%d xy=%d,%d", b.Label, rawX, rawY, x, y), false)
			b.Do()
			return
		}
	}
	log.Printf("tap missed at (%d,%d)", x, y)
	a.setLastTap(fmt.Sprintf("Miss raw=%d,%d xy=%d,%d", rawX, rawY, x, y), true)
}

// setLastTap records the most recent tap diagnostic.
// It optionally forces a redraw by clearing lastDraw before drawing under lock.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) setLastTap(msg string, redraw bool) {
	a.mu.Lock()
	a.lastTap = msg
	if redraw {
		a.lastDraw = time.Time{}
		a.drawLocked()
	}
	a.mu.Unlock()
}

// normalizeTouch maps raw input coordinates into framebuffer pixels.
// It applies optional axis swap, scales each axis from raw calibration range to screen size, applies configured inversion, and clamps to screen bounds.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) normalizeTouch(rawX, rawY int, cal touchCalibration) (int, int) {
	x, y := rawX, rawY
	minX, maxX := cal.MinX, cal.MaxX
	minY, maxY := cal.MinY, cal.MaxY
	if a.cfg.TouchSwapXY {
		x, y = y, x
		minX, maxX, minY, maxY = minY, maxY, minX, maxX
	}
	x = scaleTouchAxis(x, minX, maxX, a.cfg.ScreenWidth)
	y = scaleTouchAxis(y, minY, maxY, a.cfg.ScreenHeight)
	if a.cfg.TouchInvertX {
		x = a.cfg.ScreenWidth - x
	}
	if a.cfg.TouchInvertY {
		y = a.cfg.ScreenHeight - y
	}
	return clamp(x, 0, a.cfg.ScreenWidth), clamp(y, 0, a.cfg.ScreenHeight)
}

// scaleTouchAxis linearly maps one raw touch axis to screen pixels.
// It uses (value-min)*screen/(max-min) and clamps directly when calibration is invalid.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func scaleTouchAxis(v, rawMin, rawMax, screen int) int {
	if rawMax <= rawMin {
		return clamp(v, 0, screen)
	}
	return (v - rawMin) * screen / (rawMax - rawMin)
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
		return strconv.FormatInt(time.Now().UnixNano(), 36)
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

// artistNames joins Spotify artist names for display.
// It extracts each name and returns a comma-separated string.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func artistNames(in []artist) string {
	var names []string
	for _, a := range in {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}

// fmtProgress formats current and total playback time.
// It converts both millisecond values to m:ss and joins them with a slash.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func fmtProgress(pos, total int) string {
	return fmtMS(pos) + "/" + fmtMS(total)
}

// fmtMS formats milliseconds as m:ss.
// It truncates to whole seconds and pads seconds to two digits.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func fmtMS(ms int) string {
	sec := ms / 1000
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// playText converts playback state into display text.
// It returns Playing for true and Paused for false.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func playText(v bool) string {
	if v {
		return "Playing"
	}
	return "Paused"
}

// safe prepares text for constrained Kindle display columns.
// It removes newlines, truncates by rune count, and appends an ellipsis when the text exceeds the requested width.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func safe(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-3]) + "..."
}

// xToCol converts framebuffer X pixels to an eips text column.
// It divides by the configured eips column width.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) xToCol(x int) int { return x / a.cfg.EipsColWidth }

// yToRow converts framebuffer Y pixels to an eips text row.
// It divides by the configured eips row height.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) yToRow(y int) int { return y / a.cfg.EipsRowHeight }

// rowToY converts an eips text row to a framebuffer Y coordinate.
// It multiplies by the configured row height for touch hit boxes.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func (a *app) rowToY(row int) int {
	return row * a.cfg.EipsRowHeight
}

// clamp constrains an integer to an inclusive range.
// It returns lo below the range, hi above the range, or the original value inside the range.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// max returns the larger of two integers.
// It is used for default floors such as refresh interval and callback port.
// Parameters are interpreted according to the signature; HTTP handlers receive response/request objects, while control helpers receive action, endpoint, coordinate, or formatting values.
// Return values follow the signature: errors report caller-visible failure conditions, strings and structs carry computed display, token, or response data, and void functions communicate by side effect.
// Side effects can include Spotify HTTP calls, data/*.json reads or writes, Kindle display updates, log writes, browser launches, goroutine/channel activity, or app state mutation.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
