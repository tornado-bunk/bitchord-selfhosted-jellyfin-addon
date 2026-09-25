package jellyfintest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rairulyle/bitchord-selfhosted-addon/internal/jellyfin"
)

const Token = "fake-jellyfin-token"

type Request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
}

type Fake struct {
	URL          string
	Users        []jellyfin.User
	Folders      []jellyfin.VirtualFolder
	Tracks       map[string][]jellyfin.Item
	Files        map[string][]byte
	Extra        map[string]http.HandlerFunc
	IgnorePaging bool

	mu       sync.Mutex
	status   int
	rawBody  string
	requests []Request
}

func New(t testing.TB) *Fake {
	f := &Fake{
		Users:   Users(),
		Folders: Folders(),
		Tracks:  Tracks(),
		Files:   Files(),
		Extra:   map[string]http.HandlerFunc{},
	}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	f.URL = server.URL
	return f
}

func (f *Fake) FailWith(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *Fake) AnswerRaw(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rawBody = body
}

func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, Request{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()})
	status, rawBody := f.status, f.rawBody
	f.mu.Unlock()

	auth := r.Header.Get("Authorization")
	emby := r.Header.Get("X-Emby-Token")
	hasValidAuth := emby == Token || strings.Contains(auth, `Token="`+Token+`"`)
	if !hasValidAuth {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if rawBody != "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(rawBody))
		return
	}
	if extra, ok := f.Extra[r.URL.Path]; ok {
		extra(w, r)
		return
	}

	path := r.URL.Path
	switch {
	case path == "/Users":
		writeJSON(w, f.Users)
	case path == "/Library/VirtualFolders":
		writeJSON(w, f.Folders)
	case path == "/Items":
		f.serveItems(w, r)
	case strings.HasPrefix(path, "/Items/") && strings.HasSuffix(path, "/Images/Primary"):
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write([]byte("jpeg:" + path))
	case strings.HasPrefix(path, "/Items/"):
		id := strings.TrimPrefix(path, "/Items/")
		f.serveSingleItem(w, id)
	case strings.HasPrefix(path, "/Audio/") && strings.HasSuffix(path, "/stream"):
		parts := strings.Split(strings.TrimPrefix(path, "/Audio/"), "/")
		f.serveAudioStream(w, r, parts[0])
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *Fake) serveItems(w http.ResponseWriter, r *http.Request) {
	parentID := r.URL.Query().Get("ParentId")
	all := f.Tracks[parentID]
	if parentID == "" {
		for _, tracks := range f.Tracks {
			all = append(all, tracks...)
		}
	}
	if f.IgnorePaging {
		writeJSON(w, map[string]any{
			"Items":            all,
			"TotalRecordCount": len(all),
		})
		return
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
	limit, err := strconv.Atoi(r.URL.Query().Get("Limit"))
	if err != nil || limit <= 0 {
		limit = len(all)
	}
	start = min(start, len(all))
	end := min(start+limit, len(all))
	writeJSON(w, map[string]any{
		"Items":            all[start:end],
		"TotalRecordCount": len(all),
	})
}

func (f *Fake) serveSingleItem(w http.ResponseWriter, id string) {
	for _, tracks := range f.Tracks {
		for _, item := range tracks {
			if item.ID == id {
				writeJSON(w, item)
				return
			}
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

func (f *Fake) serveAudioStream(w http.ResponseWriter, r *http.Request, id string) {
	body, ok := f.Files[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	contentType := "audio/flac"
	if strings.HasSuffix(id, "mp3") || id == "jf-102" || id == "jf-104" {
		contentType = "audio/mpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Etag", `"fake-etag"`)
	w.Header().Set("Last-Modified", "Fri, 02 Jan 2026 03:04:05 GMT")
	http.ServeContent(w, r, "track.audio", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), bytes.NewReader(body))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func Users() []jellyfin.User {
	var admin jellyfin.User
	admin.ID = "user-admin-1"
	admin.Name = "AdminUser"
	admin.Policy.IsAdministrator = true

	var member jellyfin.User
	member.ID = "user-member-2"
	member.Name = "RegularUser"
	member.Policy.IsAdministrator = false

	return []jellyfin.User{admin, member}
}

func Folders() []jellyfin.VirtualFolder {
	return []jellyfin.VirtualFolder{
		{Name: "Music", CollectionType: "music", ItemID: "lib-music-1"},
		{Name: "Secondary Music", CollectionType: "music", ItemID: "lib-music-2"},
		{Name: "Movies", CollectionType: "movies", ItemID: "lib-movies-3"},
	}
}

func Tracks() map[string][]jellyfin.Item {
	return map[string][]jellyfin.Item{
		"lib-music-1": {
			{
				ID:           "jf-101",
				Name:         "New Religion",
				RunTimeTicks: 1840000000,
				Container:    "flac",
				Artists:      []string{"All Time Low", "Teddy Swims"},
				AlbumArtist:  "All Time Low",
				Album:        "Tell Me I'm Alive",
				AlbumID:      "album-1",
				ImageTags:    map[string]string{"Primary": "tag-101"},
				MediaStreams: []jellyfin.MediaStream{
					{Type: "Audio", Codec: "flac", BitRate: 1875000, SampleRate: 48000, BitDepth: 24},
				},
			},
			{
				ID:                   "jf-102",
				Name:                 "Weightless",
				RunTimeTicks:         1980000000,
				Container:            "mp3",
				Artists:              []string{"All Time Low"},
				AlbumArtist:          "All Time Low",
				Album:                "Nothing Personal",
				AlbumID:              "album-2",
				AlbumPrimaryImageTag: "tag-album-2",
				MediaStreams: []jellyfin.MediaStream{
					{Type: "Audio", Codec: "mp3", BitRate: 320000, SampleRate: 44100},
				},
			},
			{
				ID:           "jf-103",
				Name:         "Damned If I Do Ya",
				RunTimeTicks: 1870000000,
				Container:    "wav",
				Artists:      []string{"All Time Low"},
				AlbumArtist:  "All Time Low",
				Album:        "Nothing Personal",
				AlbumID:      "album-2",
				MediaStreams: []jellyfin.MediaStream{
					{Type: "Audio", Codec: "pcm", BitRate: 1411000, SampleRate: 44100, BitDepth: 16},
				},
			},
			{
				ID:           "jf-104",
				Name:         "Lost In Stereo",
				RunTimeTicks: 2270000000,
				Container:    "mp3",
				Artists:      []string{"All Time Low"},
				AlbumArtist:  "All Time Low",
				Album:        "Nothing Personal",
				AlbumID:      "album-2",
				MediaStreams: []jellyfin.MediaStream{
					{Type: "Audio", Codec: "mp3", BitRate: 251000},
				},
			},
			{
				ID:           "jf-106",
				Name:         "Track With No Media",
				RunTimeTicks: 1200000000,
			},
		},
		"lib-music-2": {
			{
				ID:           "jf-501",
				Name:         "Calm Down",
				RunTimeTicks: 2390000000,
				Container:    "flac",
				Artists:      []string{"Rema"},
				AlbumArtist:  "Rema",
				Album:        "Rave & Roses",
				AlbumID:      "album-rema",
				ImageTags:    map[string]string{"Primary": "tag-501"},
				MediaStreams: []jellyfin.MediaStream{
					{Type: "Audio", Codec: "flac", BitRate: 980000, SampleRate: 44100, BitDepth: 16},
				},
			},
		},
	}
}

func Files() map[string][]byte {
	return map[string][]byte{
		"jf-101": []byte("0123456789abcdefghijklmnopqrstuvwxyz"),
		"jf-102": []byte("mp3-data-weightless-stream-bytes"),
		"jf-104": []byte("mp3-data-lost-in-stereo"),
		"jf-501": []byte("flac-data-calm-down-rema"),
	}
}
