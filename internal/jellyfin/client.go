package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rairulyle/bitchord-selfhosted-addon/internal/library"
)

var (
	ErrNotFound     = errors.New("jellyfin: not found")
	ErrUnauthorized = errors.New("jellyfin: token rejected")
)

type Options struct {
	BaseURL       string
	Token         string
	UserID        string
	User          string
	PageSize      int
	HeaderTimeout time.Duration
	CallTimeout   time.Duration
}

type Client struct {
	baseURL      string
	token        string
	userID       string
	user         string
	pageSize     int
	callTimeout  time.Duration
	http         *http.Client
	idPattern    *regexp.Regexp
	mu           sync.Mutex
	cachedUserID string
}

func New(o Options) *Client {
	if o.PageSize <= 0 {
		o.PageSize = 1000
	}
	if o.HeaderTimeout <= 0 {
		o.HeaderTimeout = 10 * time.Second
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = o.HeaderTimeout
	return &Client{
		baseURL:     strings.TrimRight(o.BaseURL, "/"),
		token:       o.Token,
		userID:      o.UserID,
		user:        o.User,
		pageSize:    o.PageSize,
		callTimeout: o.CallTimeout,
		http:        &http.Client{Transport: transport},
		idPattern:   regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`),
	}
}

func (c *Client) Name() string { return "Jellyfin" }

func (c *Client) ManifestID() string { return "app.bitchord-selfhosted-addon.jellyfin" }

func (c *Client) ManifestDescription() string { return "Your Jellyfin music library" }

func (c *Client) ValidID(id string) bool { return c.idPattern.MatchString(id) }

func (c *Client) FormatError(err error) string {
	if errors.Is(err, ErrUnauthorized) {
		return "jellyfin rejected the token, check JELLYFIN_API_KEY"
	}
	return "jellyfin request failed: " + err.Error()
}

func (c *Client) Open(ctx context.Context, method, path string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	for key, values := range header {
		req.Header[key] = values
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Token="%s"`, c.token))
	req.Header.Set("X-Emby-Token", c.token)
	req.Header.Set("Accept-Encoding", "identity")
	return c.http.Do(req)
}

func (c *Client) getJSON(ctx context.Context, path string, header http.Header, into any) error {
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()
	if header == nil {
		header = http.Header{}
	}
	header.Set("Accept", "application/json")
	res, err := c.Open(ctx, http.MethodGet, path, header)
	if err != nil {
		return fmt.Errorf("jellyfin: %w", err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case res.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("jellyfin: unexpected status %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		return fmt.Errorf("jellyfin: malformed answer: %w", err)
	}
	return nil
}

func (c *Client) ResolveUserID(ctx context.Context) (string, error) {
	if c.userID != "" {
		return c.userID, nil
	}
	c.mu.Lock()
	if c.cachedUserID != "" {
		defer c.mu.Unlock()
		return c.cachedUserID, nil
	}
	c.mu.Unlock()

	var users []User
	if err := c.getJSON(ctx, "/Users", nil, &users); err != nil {
		return "", err
	}
	if len(users) == 0 {
		return "", errors.New("jellyfin: no users found on server")
	}

	var chosen string
	if c.user != "" {
		for _, u := range users {
			if strings.EqualFold(u.Name, c.user) || u.ID == c.user {
				chosen = u.ID
				break
			}
		}
		if chosen == "" {
			return "", fmt.Errorf("jellyfin: user %q not found", c.user)
		}
	} else {
		for _, u := range users {
			if u.Policy.IsAdministrator {
				chosen = u.ID
				break
			}
		}
		if chosen == "" {
			chosen = users[0].ID
		}
	}

	c.mu.Lock()
	c.cachedUserID = chosen
	c.mu.Unlock()
	return chosen, nil
}

func (c *Client) MusicLibraries(ctx context.Context, filter string) ([]VirtualFolder, error) {
	var folders []VirtualFolder
	if err := c.getJSON(ctx, "/Library/VirtualFolders", nil, &folders); err != nil {
		return nil, err
	}
	var musicFolders []VirtualFolder
	for _, f := range folders {
		if strings.EqualFold(f.CollectionType, "music") {
			musicFolders = append(musicFolders, f)
		}
	}
	if len(musicFolders) == 0 {
		musicFolders = folders
	}
	var out []VirtualFolder
	for _, f := range musicFolders {
		if filter == "" || strings.EqualFold(f.Name, filter) || f.ItemID == filter {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("jellyfin: no music library matches %q", filter)
	}
	return out, nil
}

func (c *Client) AllTracks(ctx context.Context, filter string) ([]library.Track, error) {
	userID, err := c.ResolveUserID(ctx)
	if err != nil {
		return nil, err
	}
	libraries, err := c.MusicLibraries(ctx, filter)
	if err != nil {
		return nil, err
	}

	var out []library.Track
	for _, lib := range libraries {
		for start := 0; ; {
			page, total, err := c.trackPage(ctx, userID, lib.ItemID, start)
			if err != nil {
				return nil, err
			}
			if len(page) == 0 {
				break
			}
			for _, item := range page {
				if tr, ok := FromJellyfin(item); ok {
					out = append(out, tr)
				}
			}
			start += len(page)
			if (total > 0 && start >= total) || len(page) < c.pageSize {
				break
			}
		}
	}
	return out, nil
}

func (c *Client) AllLibraryTracks(ctx context.Context, section string) ([]library.Track, error) {
	return c.AllTracks(ctx, section)
}

func (c *Client) trackPage(ctx context.Context, userID, parentID string, start int) ([]Item, int, error) {
	var answer struct {
		Items            []Item `json:"Items"`
		TotalRecordCount int    `json:"TotalRecordCount"`
	}
	v := url.Values{}
	v.Set("ParentId", parentID)
	v.Set("IncludeItemTypes", "Audio")
	v.Set("Recursive", "true")
	v.Set("Fields", "MediaSources,MediaStreams,Container")
	v.Set("StartIndex", strconv.Itoa(start))
	v.Set("Limit", strconv.Itoa(c.pageSize))
	if userID != "" {
		v.Set("UserId", userID)
	}
	path := "/Items?" + v.Encode()
	if err := c.getJSON(ctx, path, nil, &answer); err != nil {
		return nil, 0, err
	}
	return answer.Items, answer.TotalRecordCount, nil
}

func (c *Client) Track(ctx context.Context, id string) (Item, error) {
	userID, _ := c.ResolveUserID(ctx)
	path := "/Items/" + url.PathEscape(id) + "?Fields=MediaSources,MediaStreams,Container"
	if userID != "" {
		path += "&UserId=" + url.QueryEscape(userID)
	}
	var item Item
	if err := c.getJSON(ctx, path, nil, &item); err != nil {
		return Item{}, err
	}
	if item.ID == "" {
		return Item{}, ErrNotFound
	}
	return item, nil
}

func (c *Client) StreamInfo(ctx context.Context, id string) (library.StreamInfo, error) {
	item, err := c.Track(ctx, id)
	if err != nil {
		return library.StreamInfo{}, err
	}
	audioStream, _ := item.AudioStream()
	mediaSource, _ := item.FirstMediaSource()

	container := strings.ToLower(item.Container)
	if container == "" {
		container = strings.ToLower(mediaSource.Container)
	}
	codec := strings.ToLower(audioStream.Codec)
	if codec == "" {
		codec = container
	}
	format := AudioFormat(codec, container)

	bitrate := audioStream.BitRate
	if bitrate == 0 {
		bitrate = mediaSource.Bitrate
	}
	kbps := (bitrate + 500) / 1000

	label := item.Name
	if len(item.Artists) > 0 {
		label += " — " + strings.Join(item.Artists, ", ")
	} else if item.AlbumArtist != "" {
		label += " — " + item.AlbumArtist
	}

	thumb := ""
	if item.ImageTags != nil && item.ImageTags["Primary"] != "" {
		thumb = ArtPath(item.ID, item.ImageTags["Primary"])
	} else if item.AlbumPrimaryImageTag != "" && item.AlbumID != "" {
		thumb = ArtPath(item.AlbumID, item.AlbumPrimaryImageTag)
	}

	return library.StreamInfo{
		ID:         item.ID,
		Format:     format,
		Quality:    Quality(format, kbps, audioStream.SampleRate, audioStream.BitDepth),
		Codec:      format,
		Container:  container,
		SampleRate: audioStream.SampleRate,
		BitDepth:   audioStream.BitDepth,
		Bitrate:    bitrate,
		Label:      label,
		PartKey:    StreamPath(item.ID),
		Thumb:      thumb,
	}, nil
}

func (c *Client) OpenFile(ctx context.Context, method, id, partKey string, header http.Header) (*http.Response, error) {
	if partKey == "" {
		partKey = StreamPath(id)
	}
	return c.Open(ctx, method, partKey, header)
}

func (c *Client) OpenArt(ctx context.Context, id string, thumb string) (*http.Response, error) {
	if thumb == "" {
		item, err := c.Track(ctx, id)
		if err != nil {
			return nil, err
		}
		if item.ImageTags != nil && item.ImageTags["Primary"] != "" {
			thumb = ArtPath(item.ID, item.ImageTags["Primary"])
		} else if item.AlbumPrimaryImageTag != "" && item.AlbumID != "" {
			thumb = ArtPath(item.AlbumID, item.AlbumPrimaryImageTag)
		} else {
			return nil, ErrNotFound
		}
	}
	return c.Open(ctx, http.MethodGet, thumb, nil)
}
