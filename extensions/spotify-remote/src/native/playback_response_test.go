package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

type kualResponseTransport func(*http.Request) (*http.Response, error)

func (f kualResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestKUALVolumeRequiresPlaybackState(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        io.Reader
		status      int
		wantVolume  string
		wantMessage string
	}{
		{"empty OK", strings.NewReader(""), 200, "", "Volume failed"},
		{"null OK", strings.NewReader(" null\n"), 200, "", "Volume failed"},
		{"interrupted body", io.MultiReader(strings.NewReader(`{"device":{"volume_percent":65}}`), iotest.ErrReader(io.ErrUnexpectedEOF)), 200, "", "Volume failed"},
		{"invalid JSON", strings.NewReader(`{"device":`), 200, "", "Volume failed"},
		{"no active device", strings.NewReader(""), 204, "", "No active Spotify device"},
		{"active device", strings.NewReader(`{"device":{"volume_percent":65}}`), 200, "75", "Volume 75 sent"},
		{"muted device", strings.NewReader(`{"device":{"volume_percent":0}}`), 200, "10", "Volume 10 sent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var commands []string
			client := &http.Client{Transport: kualResponseTransport(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/me/player":
					return &http.Response{StatusCode: tc.status, Body: io.NopCloser(tc.body)}, nil
				case r.Method == http.MethodPut && r.URL.Path == "/v1/me/player/volume":
					commands = append(commands, r.URL.Query().Get("volume_percent"))
					return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
					return nil, nil
				}
			})}
			a := newNativeSpotifyAPITestApp(t, client)
			a.runKUAL("volup")
			if tc.wantVolume == "" && len(commands) != 0 {
				t.Errorf("sent volume commands %v without playback state", commands)
			}
			if tc.wantVolume != "" && (len(commands) != 1 || commands[0] != tc.wantVolume) {
				t.Errorf("sent volume commands %v, want [%s]", commands, tc.wantVolume)
			}
			status, err := os.ReadFile(filepath.Join(a.base, "data", "status.txt"))
			if err != nil || !strings.Contains(string(status), tc.wantMessage) {
				t.Errorf("status.txt = %q, err = %v, want %q", status, err, tc.wantMessage)
			}
		})
	}
}

func TestKUALNowPlayingSnapshotRecoversAfterInterruptedRead(t *testing.T) {
	var reply io.Reader
	client := &http.Client{Transport: kualResponseTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/me/player" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(reply)}, nil
	})}
	first := newNativeSpotifyAPITestApp(t, client)
	for _, step := range []struct {
		body  io.Reader
		title string
	}{
		{strings.NewReader(`{"item":{"name":"First song"}}`), "First song"},
		{iotest.ErrReader(io.ErrUnexpectedEOF), ""},
		{strings.NewReader(`{"item":{"name":"Next song"}}`), "Next song"},
	} {
		reply = step.body
		// KUAL starts a new process for each action against the same data directory.
		next := &app{base: first.base, cfg: first.cfg, client: client}
		next.runKUAL("nowplaying")
		var snapshot struct {
			OK    bool
			Title string
			Error string
		}
		if err := readJSON(filepath.Join(first.base, "data", "nowplaying.json"), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.OK != (step.title != "") || snapshot.Title != step.title || (snapshot.Error == "") != snapshot.OK {
			t.Fatalf("snapshot after %q = %+v", step.title, snapshot)
		}
	}
}
