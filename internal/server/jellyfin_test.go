package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rairulyle/bitchord-selfhosted-addon/internal/jellyfin"
	"github.com/rairulyle/bitchord-selfhosted-addon/internal/jellyfintest"
	"github.com/rairulyle/bitchord-selfhosted-addon/internal/library"
)

type jellyfinHarness struct {
	fake    *jellyfintest.Fake
	server  *server
	handler http.Handler
	logs    *syncBuffer
}

func newJellyfinHarness(t *testing.T) *jellyfinHarness {
	return newJellyfinHarnessWith(t, jellyfin.Options{}, true)
}

func newJellyfinHarnessWith(t *testing.T, options jellyfin.Options, load bool) *jellyfinHarness {
	t.Helper()
	fake := jellyfintest.New(t)
	options.BaseURL, options.Token = fake.URL, jellyfintest.Token
	client := jellyfin.New(options)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	lib := library.NewLibrary(client, "Music", time.Hour, log)
	if load {
		if err := lib.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
	}
	s := newServer(Options{
		Secret: testSecret, PublicURL: testPublic, AddonName: "Home Jellyfin", Version: "1.2.3",
		Library: lib, Backend: client, Log: log,
	})
	return &jellyfinHarness{fake: fake, server: s, handler: s.handler(), logs: logs}
}

func (h *jellyfinHarness) do(method, path string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for key, values := range header {
		req.Header[key] = values
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *jellyfinHarness) get(path string) *httptest.ResponseRecorder {
	return h.do(http.MethodGet, path, nil)
}

func TestJellyfinManifest(t *testing.T) {
	h := newJellyfinHarness(t)
	rec := h.get("/" + testSecret + "/manifest.json")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	got := decode[map[string]any](t, rec)
	want := map[string]any{
		"id": "app.bitchord-selfhosted-addon.jellyfin", "name": "Home Jellyfin", "version": "1.2.3",
		"description": "Your Jellyfin music library", "contentType": "music",
		"resources": []any{"search", "stream"}, "types": []any{"track"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest = %v, want %v", got, want)
	}
}

func TestJellyfinSearch(t *testing.T) {
	h := newJellyfinHarness(t)
	rec := h.get("/" + testSecret + "/search?q=new+religion")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got := decode[searchJSON](t, rec)
	if len(got.Tracks) != 1 {
		t.Fatalf("tracks returned = %d, want 1", len(got.Tracks))
	}
	track := got.Tracks[0]
	if track.ID != "jf-101" || track.Title != "New Religion" || track.AudioQuality != "LOSSLESS" || track.Format != "flac" {
		t.Fatalf("track = %+v", track)
	}
	if track.ArtworkURL != testBase+"/art/jf-101" {
		t.Errorf("artworkURL = %q", track.ArtworkURL)
	}
}

func TestJellyfinStream(t *testing.T) {
	h := newJellyfinHarness(t)

	t.Run("flac descriptor", func(t *testing.T) {
		rec := h.get("/" + testSecret + "/stream/jf-101")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		got := decode[map[string]any](t, rec)
		want := map[string]any{
			"url": testBase + "/file/jf-101", "format": "flac", "quality": "lossless 24-bit 48kHz", "codec": "flac",
			"container": "flac", "manifest": "none", "sampleRate": float64(48000), "bitDepth": float64(24), "bitrate": float64(1875000),
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("descriptor = %v, want %v", got, want)
		}
	})

	t.Run("mp3 descriptor", func(t *testing.T) {
		rec := h.get("/" + testSecret + "/stream/jf-102")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		got := decode[map[string]any](t, rec)
		want := map[string]any{
			"url": testBase + "/file/jf-102", "format": "mp3", "quality": "320kbps", "codec": "mp3",
			"container": "mp3", "manifest": "none", "sampleRate": float64(44100), "bitrate": float64(320000),
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("descriptor = %v, want %v", got, want)
		}
	})
}

func TestJellyfinFileWithRange(t *testing.T) {
	h := newJellyfinHarness(t)

	rec := h.get("/" + testSecret + "/file/jf-101")
	if rec.Code != http.StatusOK || rec.Body.String() != flacBody {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}

	rangeRec := h.do(http.MethodGet, "/"+testSecret+"/file/jf-101", http.Header{"Range": {"bytes=10-19"}})
	if rangeRec.Code != http.StatusPartialContent || rangeRec.Body.String() != "abcdefghij" {
		t.Fatalf("status %d, body %q", rangeRec.Code, rangeRec.Body.String())
	}
}

func TestJellyfinArt(t *testing.T) {
	h := newJellyfinHarness(t)

	rec := h.get("/" + testSecret + "/art/jf-101")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "jpeg:") {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}

	albumArtRec := h.get("/" + testSecret + "/art/jf-102")
	if albumArtRec.Code != http.StatusOK || !strings.Contains(albumArtRec.Body.String(), "jpeg:") {
		t.Fatalf("status %d, body %q", albumArtRec.Code, albumArtRec.Body.String())
	}

	noThumbRec := h.get("/" + testSecret + "/art/jf-104")
	if noThumbRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for track without thumb, got %d", noThumbRec.Code)
	}
}

func TestJellyfinAuthFailureLogs(t *testing.T) {
	h := newJellyfinHarness(t)
	h.server.Backend = jellyfin.New(jellyfin.Options{BaseURL: h.fake.URL, Token: "wrong"})
	rec := h.get("/" + testSecret + "/stream/jf-101")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "JELLYFIN_API_KEY") {
		t.Fatalf("expected mention of JELLYFIN_API_KEY in logs: %s", logs)
	}
}
