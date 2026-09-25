package jellyfin_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rairulyle/bitchord-selfhosted-addon/internal/jellyfin"
	"github.com/rairulyle/bitchord-selfhosted-addon/internal/jellyfintest"
)

func client(fake *jellyfintest.Fake, pageSize int) *jellyfin.Client {
	return jellyfin.New(jellyfin.Options{BaseURL: fake.URL, Token: jellyfintest.Token, PageSize: pageSize})
}

func keys(tracks []string) string {
	return strings.Join(tracks, ",")
}

func trackIDs(tracks []any) []string {
	out := make([]string, len(tracks))
	for i, t := range tracks {
		out[i] = t.(interface{ GetID() string }).GetID()
	}
	return out
}

func TestAllTracksPagesThroughEveryMusicSection(t *testing.T) {
	fake := jellyfintest.New(t)
	c := client(fake, 2)
	tracks, err := c.AllTracks(context.Background(), "")
	if err != nil {
		t.Fatalf("AllTracks: %v", err)
	}
	var ids []string
	for _, tr := range tracks {
		ids = append(ids, tr.ID)
	}
	if got := keys(ids); got != "jf-101,jf-102,jf-103,jf-104,jf-501" {
		t.Fatalf("ids = %s", got)
	}
}

func TestAllTracksFiltersSections(t *testing.T) {
	cases := map[string]struct{ filter, want string }{
		"by id":    {"lib-music-2", "jf-501"},
		"by title": {"Music", "jf-101,jf-102,jf-103,jf-104"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracks, err := client(jellyfintest.New(t), 1000).AllTracks(context.Background(), tc.filter)
			if err != nil {
				t.Fatalf("AllTracks: %v", err)
			}
			var ids []string
			for _, tr := range tracks {
				ids = append(ids, tr.ID)
			}
			if got := keys(ids); got != tc.want {
				t.Fatalf("ids = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAllTracksFailsWhenNoLibraryMatches(t *testing.T) {
	fake := jellyfintest.New(t)
	c := client(fake, 1000)
	for _, filter := range []string{"NonExistent", "Movies"} {
		_, err := c.AllTracks(context.Background(), filter)
		if err == nil || !strings.Contains(err.Error(), filter) {
			t.Errorf("filter %q: err = %v, want mention of filter", filter, err)
		}
	}
}

func TestEveryRequestCarriesTokenAsHeaderOnly(t *testing.T) {
	fake := jellyfintest.New(t)
	c := client(fake, 1000)
	if _, err := c.AllTracks(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Track(context.Background(), "jf-101"); err != nil {
		t.Fatal(err)
	}
	for _, r := range fake.Requests() {
		hasAuth := r.Header.Get("X-Emby-Token") == jellyfintest.Token ||
			strings.Contains(r.Header.Get("Authorization"), jellyfintest.Token)
		if !hasAuth {
			t.Errorf("%s: auth header missing", r.Path)
		}
		if strings.Contains(r.Query, jellyfintest.Token) || strings.Contains(r.Path, jellyfintest.Token) {
			t.Errorf("%s?%s: token leaked into URL", r.Path, r.Query)
		}
	}
}

func TestResolveUser(t *testing.T) {
	t.Run("defaults to admin", func(t *testing.T) {
		fake := jellyfintest.New(t)
		c := jellyfin.New(jellyfin.Options{BaseURL: fake.URL, Token: jellyfintest.Token})
		uid, err := c.ResolveUserID(context.Background())
		if err != nil || uid != "user-admin-1" {
			t.Fatalf("ResolveUserID = %q, err %v", uid, err)
		}
	})
	t.Run("resolves by name", func(t *testing.T) {
		fake := jellyfintest.New(t)
		c := jellyfin.New(jellyfin.Options{BaseURL: fake.URL, Token: jellyfintest.Token, User: "RegularUser"})
		uid, err := c.ResolveUserID(context.Background())
		if err != nil || uid != "user-member-2" {
			t.Fatalf("ResolveUserID = %q, err %v", uid, err)
		}
	})
	t.Run("uses explicit userID", func(t *testing.T) {
		fake := jellyfintest.New(t)
		c := jellyfin.New(jellyfin.Options{BaseURL: fake.URL, Token: jellyfintest.Token, UserID: "custom-id"})
		uid, err := c.ResolveUserID(context.Background())
		if err != nil || uid != "custom-id" {
			t.Fatalf("ResolveUserID = %q, err %v", uid, err)
		}
	})
}

func TestTrackAndStreamInfo(t *testing.T) {
	fake := jellyfintest.New(t)
	c := client(fake, 1000)

	t.Run("hi-res flac", func(t *testing.T) {
		info, err := c.StreamInfo(context.Background(), "jf-101")
		if err != nil {
			t.Fatalf("StreamInfo: %v", err)
		}
		if info.Format != "flac" || info.Quality != "lossless 24-bit 48kHz" || info.Bitrate != 1875000 {
			t.Fatalf("info = %+v", info)
		}
		if info.Label != "New Religion — All Time Low, Teddy Swims" {
			t.Fatalf("label = %q", info.Label)
		}
	})

	t.Run("mp3", func(t *testing.T) {
		info, err := c.StreamInfo(context.Background(), "jf-102")
		if err != nil {
			t.Fatalf("StreamInfo: %v", err)
		}
		if info.Format != "mp3" || info.Quality != "320kbps" || info.SampleRate != 44100 {
			t.Fatalf("info = %+v", info)
		}
	})

	t.Run("wav", func(t *testing.T) {
		info, err := c.StreamInfo(context.Background(), "jf-103")
		if err != nil {
			t.Fatalf("StreamInfo: %v", err)
		}
		if info.Format != "wav" || info.Quality != "lossless 16-bit 44.1kHz" {
			t.Fatalf("info = %+v", info)
		}
	})
}

func TestOpenFileAndArt(t *testing.T) {
	fake := jellyfintest.New(t)
	c := client(fake, 1000)

	res, err := c.OpenFile(context.Background(), http.MethodGet, "jf-101", "", nil)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(body) != "0123456789abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("status %d, body %s", res.StatusCode, body)
	}

	artRes, err := c.OpenArt(context.Background(), "jf-101", "")
	if err != nil {
		t.Fatalf("OpenArt: %v", err)
	}
	defer artRes.Body.Close()
	artBody, _ := io.ReadAll(artRes.Body)
	if artRes.StatusCode != http.StatusOK || !strings.Contains(string(artBody), "jpeg:") {
		t.Fatalf("art status %d, body %s", artRes.StatusCode, artBody)
	}
}

func TestErrors(t *testing.T) {
	fake := jellyfintest.New(t)
	c := client(fake, 1000)

	_, err := c.Track(context.Background(), "unknown-id")
	if !errors.Is(err, jellyfin.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	fake.FailWith(http.StatusUnauthorized)
	_, err = c.Track(context.Background(), "jf-101")
	if !errors.Is(err, jellyfin.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}
