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
	"syscall"
	"time"
)

const (
	scopes                     = "user-read-playback-state user-modify-playback-state user-read-currently-playing"
	placeholderSpotifyClientID = "PASTE_SPOTIFY_CLIENT_ID_HERE"
)

type config struct {
	ClientID          string `json:"client_id"`
	Redirect          string `json:"redirect_uri"`
	Port              int    `json:"port"`
	RefreshSec        int    `json:"refresh_seconds"`
	ScreenWidth       int    `json:"screen_width"`
	ScreenHeight      int    `json:"screen_height"`
	TouchMinX         int    `json:"touch_min_x"`
	TouchMaxX         int    `json:"touch_max_x"`
	TouchMinY         int    `json:"touch_min_y"`
	TouchMaxY         int    `json:"touch_max_y"`
	TouchSwapXY       bool   `json:"touch_swap_xy"`
	TouchInvertX      bool   `json:"touch_invert_x"`
	TouchInvertY      bool   `json:"touch_invert_y"`
	TouchUseKernelAbs *bool  `json:"touch_use_kernel_abs"`
	EipsColWidth      int    `json:"eips_col_width"`
	EipsRowHeight     int    `json:"eips_row_height"`
	ButtonTop         int    `json:"button_top"`
	ButtonHeight      int    `json:"button_height"`
	ButtonGap         int    `json:"button_gap"`
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

type artist struct {
	Name string `json:"name"`
}

type albumImage struct {
	URL    string `json:"url"`
	Height int    `json:"height"`
	Width  int    `json:"width"`
}

type album struct {
	Name   string       `json:"name"`
	Images []albumImage `json:"images"`
}

type track struct {
	Name       string   `json:"name"`
	Artists    []artist `json:"artists"`
	Album      album    `json:"album"`
	DurationMS int      `json:"duration_ms"`
}

type device struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	IsActive      bool   `json:"is_active"`
	VolumePercent int    `json:"volume_percent"`
}

type playback struct {
	Device       device `json:"device"`
	Shuffle      bool   `json:"shuffle_state"`
	Repeat       string `json:"repeat_state"`
	ProgressMS   int    `json:"progress_ms"`
	IsPlaying    bool   `json:"is_playing"`
	CurrentTrack track  `json:"item"`
}

type uiButton struct {
	Label string
	X1    int
	Y1    int
	X2    int
	Y2    int
	Do    func()
}

type uiTouchZone struct {
	Action string
	Label  string
	X1     int
	Y1     int
	X2     int
	Y2     int
}

type app struct {
	base       string
	cfg        config
	client     *http.Client
	status     string
	err        string
	state      playback
	hasState   bool
	buttons    []uiButton
	mu         sync.Mutex
	lastDraw   time.Time
	lastAction time.Time
	lastTap    string
	quit       chan struct{}
}

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

func (a *app) fbinkExitMessage() {
	eipsClear()
	a.fbinkText(3, 12, "Closing Spotify Remote")
	a.fbinkText(2, 16, "Returning to Kindle...")
	log.Printf("FBInk UI exit message drawn")
}

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
	}
}

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
	playIcon := "PLAY"
	coverPath := ""
	if err != nil {
		artist = "Failed to get playback state"
		albumName = err.Error()
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
	a.fbinkText(4, 21, safe(albumName, 24))
	a.fbinkText(4, 27, progress+"          "+duration)
	a.fbinkText(5, 31, "|<   "+playIcon+"   >|")
	eips(22, 0, "VOL +")
	eips(25, 0, volume+"%")
	eips(28, 0, "VOL -")
	a.fbinkText(3, 39, "SHUF "+shuffle+"  REP "+repeat)
	log.Printf("FBInk UI drawn: %s / %s", title, artist)
}

func (a *app) fbinkPath() string {
	for _, p := range []string{"/mnt/us/libkh/bin/fbink", "/mnt/us/koreader/fbink", "/mnt/us/extensions/MRInstaller/bin/KHF/fbink"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func (a *app) fbinkClear() {
	if p := a.fbinkPath(); p != "" {
		_ = exec.Command(p, "-q", "-f", "-c").Run()
		_ = exec.Command(p, "-q", "-k").Run()
	}
	eipsClear()
}

func (a *app) fbinkText(size, row int, text string) {
	if p := a.fbinkPath(); p != "" {
		_ = exec.Command(p, "-q", "-S", strconv.Itoa(size), "-m", "-y", strconv.Itoa(row), text).Run()
	}
}

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

func ioctlGrab(f *os.File, grab bool) error {
	val := uintptr(0)
	if grab {
		val = 1
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(0x40044590), val)
	if errno != 0 {
		return errno
	}
	return nil
}

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

func (a *app) fbinkTouchZones() []uiTouchZone {
	return []uiTouchZone{
		{Action: "volup", Label: "vol-up-left", X1: 0, Y1: 820, X2: 270, Y2: 1015},
		{Action: "voldown", Label: "vol-down-left", X1: 0, Y1: 1030, X2: 270, Y2: 1185},
		{Action: "prev", Label: "prev", X1: 0, Y1: 1190, X2: 420, Y2: 1590},
		{Action: "playpause", Label: "playpause", X1: 438, Y1: 1235, X2: 796, Y2: 1480},
		{Action: "next", Label: "next", X1: 820, Y1: 1210, X2: 1115, Y2: 1590},
		{Action: "quit", Label: "quit-corner", X1: 980, Y1: 1490, X2: 1236, Y2: 1648},
	}
}

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

func (a *app) kualStatus() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	if err != nil {
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

func (a *app) kualLogin() {
	if !validClientID(a.cfg.ClientID) {
		a.kualPrint("Spotify Client ID missing", "Edit data/config.json", "Use your own Client ID.", "Do not add a Client Secret.")
		return
	}
	verifier := randomString(64)
	state := randomString(24)
	challenge := pkceChallenge(verifier)
	if err := writeJSON(filepath.Join(a.base, "data", "oauth.json"), oauthState{State: state, CodeVerifier: verifier, CreatedAt: time.Now().Unix()}); err != nil {
		a.kualPrint("Login state error", err.Error())
		return
	}
	v := url.Values{}
	v.Set("client_id", a.cfg.ClientID)
	v.Set("response_type", "code")
	v.Set("redirect_uri", a.cfg.Redirect)
	v.Set("code_challenge_method", "S256")
	v.Set("code_challenge", challenge)
	v.Set("state", state)
	v.Set("scope", scopes)
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

func (a *app) prepareAuthURL() (string, error) {
	verifier := randomString(64)
	state := randomString(24)
	challenge := pkceChallenge(verifier)
	if err := writeJSON(filepath.Join(a.base, "data", "oauth.json"), oauthState{State: state, CodeVerifier: verifier, CreatedAt: time.Now().Unix()}); err != nil {
		return "", err
	}
	v := url.Values{}
	v.Set("client_id", a.cfg.ClientID)
	v.Set("response_type", "code")
	v.Set("redirect_uri", a.cfg.Redirect)
	v.Set("code_challenge_method", "S256")
	v.Set("code_challenge", challenge)
	v.Set("state", state)
	v.Set("scope", scopes)
	return "https://accounts.spotify.com/authorize?" + v.Encode(), nil
}

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

func (a *app) kualPlayPause() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	if err != nil || code == http.StatusNoContent {
		if err == nil {
			err = errors.New("No active Spotify device")
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

func (a *app) kualControl(method, endpoint string, body io.Reader, label string) {
	_, err := a.spotifyAPI(method, endpoint, body, nil)
	if err != nil {
		a.kualPrint(label+" failed", err.Error())
		return
	}
	a.kualPrint(label+" sent", "Run Status to refresh.")
}

func (a *app) kualVolume(delta int) {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	if err != nil || code == http.StatusNoContent {
		if err == nil {
			err = errors.New("No active Spotify device")
		}
		a.kualPrint("Volume failed", err.Error())
		return
	}
	v := clamp(p.Device.VolumePercent+delta, 0, 100)
	a.kualControl(http.MethodPut, "https://api.spotify.com/v1/me/player/volume?volume_percent="+strconv.Itoa(v), nil, "Volume "+strconv.Itoa(v))
}

func (a *app) kualToggleShuffle() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	if err != nil || code == http.StatusNoContent {
		if err == nil {
			err = errors.New("No active Spotify device")
		}
		a.kualPrint("Shuffle failed", err.Error())
		return
	}
	a.kualControl(http.MethodPut, "https://api.spotify.com/v1/me/player/shuffle?state="+strconv.FormatBool(!p.Shuffle), nil, "Shuffle")
}

func (a *app) kualToggleRepeat() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	if err != nil || code == http.StatusNoContent {
		if err == nil {
			err = errors.New("No active Spotify device")
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

func (a *app) setupLog() {
	p := filepath.Join(a.base, "logs", "spotify-remote.log")
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		log.SetOutput(f)
	}
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

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

func boolPtr(v bool) *bool {
	return &v
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

func (a *app) refresh() {
	var p playback
	code, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player", nil, &p)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.err = "Failed to get playback state: " + err.Error()
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
		a.err = err.Error()
	} else {
		a.status = action + " sent"
		a.err = ""
	}
	a.drawLocked()
	a.mu.Unlock()
	time.Sleep(900 * time.Millisecond)
	a.refresh()
}

func (a *app) showDevices() {
	var out struct {
		Devices []device `json:"devices"`
	}
	_, err := a.spotifyAPI(http.MethodGet, "https://api.spotify.com/v1/me/player/devices", nil, &out)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.err = err.Error()
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
				a.err = err.Error()
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
	verifier := randomString(64)
	state := randomString(24)
	challenge := pkceChallenge(verifier)
	if err := writeJSON(filepath.Join(a.base, "data", "oauth.json"), oauthState{State: state, CodeVerifier: verifier, CreatedAt: time.Now().Unix()}); err != nil {
		a.mu.Lock()
		a.err = err.Error()
		a.drawLocked()
		a.mu.Unlock()
		return
	}
	v := url.Values{}
	v.Set("client_id", a.cfg.ClientID)
	v.Set("response_type", "code")
	v.Set("redirect_uri", a.cfg.Redirect)
	v.Set("code_challenge_method", "S256")
	v.Set("code_challenge", challenge)
	v.Set("state", state)
	v.Set("scope", scopes)
	authURL := "https://accounts.spotify.com/authorize?" + v.Encode()
	log.Printf("auth url: %s", authURL)
	openBrowser(authURL)
}

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

func (a *app) setError(msg string) {
	a.mu.Lock()
	a.err = msg
	a.drawLocked()
	a.mu.Unlock()
}

func (a *app) exchangeCode(code, state string) error {
	if code == "" {
		return errors.New("missing authorization code")
	}
	var st oauthState
	if err := readJSON(filepath.Join(a.base, "data", "oauth.json"), &st); err != nil {
		return errors.New("login state missing; tap Login again")
	}
	if st.State != "" && state != "" && st.State != state {
		return errors.New("OAuth state mismatch")
	}
	form := url.Values{}
	form.Set("client_id", a.cfg.ClientID)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", a.cfg.Redirect)
	form.Set("code_verifier", st.CodeVerifier)
	var tok tokenFile
	if err := a.spotifyForm("https://accounts.spotify.com/api/token", form, "", &tok); err != nil {
		return err
	}
	tok.ExpiresAt = time.Now().Unix() + int64(tok.ExpiresIn) - 60
	return writeJSON(filepath.Join(a.base, "data", "token.json"), &tok)
}

func (a *app) loadToken() (tokenFile, error) {
	var tok tokenFile
	if err := readJSON(filepath.Join(a.base, "data", "token.json"), &tok); err != nil || tok.AccessToken == "" {
		return tok, errors.New("Token missing; tap Login")
	}
	if time.Now().Unix() < tok.ExpiresAt {
		return tok, nil
	}
	if tok.RefreshToken == "" {
		return tok, errors.New("Token expired; tap Login")
	}
	form := url.Values{}
	form.Set("client_id", a.cfg.ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", tok.RefreshToken)
	var refreshed tokenFile
	if err := a.spotifyForm("https://accounts.spotify.com/api/token", form, "", &refreshed); err != nil {
		return tok, fmt.Errorf("Token expired: %w", err)
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = tok.RefreshToken
	}
	refreshed.ExpiresAt = time.Now().Unix() + int64(refreshed.ExpiresIn) - 60
	if err := writeJSON(filepath.Join(a.base, "data", "token.json"), &refreshed); err != nil {
		return tok, err
	}
	return refreshed, nil
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

func spotifyError(status int, body []byte) error {
	text := string(body)
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

func (a *app) draw() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.drawLocked()
}

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

func (a *app) button(slot int, label string, do func()) uiButton {
	step := a.cfg.ButtonHeight + a.cfg.ButtonGap
	y1 := a.cfg.ButtonTop + slot*step
	return uiButton{Label: label, X1: 0, Y1: y1, X2: a.cfg.ScreenWidth, Y2: y1 + a.cfg.ButtonHeight, Do: do}
}

func eipsClear() {
	_ = exec.Command("eips", "-c").Run()
}

func eips(row, col int, text string) {
	if text == "" {
		text = " "
	}
	_ = exec.Command("eips", strconv.Itoa(row), strconv.Itoa(col), text).Run()
}

func openBrowser(raw string) {
	_ = exec.Command("lipc-set-prop", "com.lab126.appmgrd", "start", "app://com.lab126.browser?url="+raw).Run()
}

type inputEvent struct {
	Sec   int32
	Usec  int32
	Type  uint16
	Code  uint16
	Value int32
}

type touchCalibration struct {
	MinX   int
	MaxX   int
	MinY   int
	MaxY   int
	Source string
}

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

func (a *app) setLastTap(msg string, redraw bool) {
	a.mu.Lock()
	a.lastTap = msg
	if redraw {
		a.lastDraw = time.Time{}
		a.drawLocked()
	}
	a.mu.Unlock()
}

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

func scaleTouchAxis(v, rawMin, rawMax, screen int) int {
	if rawMax <= rawMin {
		return clamp(v, 0, screen)
	}
	return (v - rawMin) * screen / (rawMax - rawMin)
}

func validClientID(id string) bool {
	return id != "" && !strings.HasPrefix(id, "PASTE_")
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func artistNames(in []artist) string {
	var names []string
	for _, a := range in {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}

func fmtProgress(pos, total int) string {
	return fmtMS(pos) + "/" + fmtMS(total)
}

func fmtMS(ms int) string {
	sec := ms / 1000
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

func playText(v bool) string {
	if v {
		return "Playing"
	}
	return "Paused"
}

func safe(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-3]) + "..."
}

func (a *app) xToCol(x int) int { return x / a.cfg.EipsColWidth }
func (a *app) yToRow(y int) int { return y / a.cfg.EipsRowHeight }
func (a *app) rowToY(row int) int {
	return row * a.cfg.EipsRowHeight
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
