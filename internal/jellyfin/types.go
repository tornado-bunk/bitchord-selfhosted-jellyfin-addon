package jellyfin

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/rairulyle/bitchord-selfhosted-addon/internal/library"
)

type User struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Policy struct {
		IsAdministrator bool `json:"IsAdministrator"`
	} `json:"Policy"`
}

type VirtualFolder struct {
	Name           string   `json:"Name"`
	CollectionType string   `json:"CollectionType"`
	ItemID         string   `json:"ItemId"`
	Locations      []string `json:"Locations"`
}

type Item struct {
	ID                   string            `json:"Id"`
	Name                 string            `json:"Name"`
	RunTimeTicks         int64             `json:"RunTimeTicks"`
	Container            string            `json:"Container"`
	Artists              []string          `json:"Artists"`
	AlbumArtist          string            `json:"AlbumArtist"`
	Album                string            `json:"Album"`
	AlbumID              string            `json:"AlbumId"`
	ImageTags            map[string]string `json:"ImageTags"`
	AlbumPrimaryImageTag string            `json:"AlbumPrimaryImageTag"`
	MediaSources         []MediaSource     `json:"MediaSources"`
	MediaStreams         []MediaStream     `json:"MediaStreams"`
}

type MediaSource struct {
	ID           string        `json:"Id"`
	Container    string        `json:"Container"`
	Bitrate      int           `json:"Bitrate"`
	MediaStreams []MediaStream `json:"MediaStreams"`
}

type MediaStream struct {
	Type       string `json:"Type"`
	Codec      string `json:"Codec"`
	BitRate    int    `json:"BitRate"`
	SampleRate int    `json:"SampleRate"`
	BitDepth   int    `json:"BitDepth"`
}

func (item Item) AudioStream() (MediaStream, bool) {
	for _, s := range item.MediaStreams {
		if strings.EqualFold(s.Type, "Audio") {
			return s, true
		}
	}
	for _, ms := range item.MediaSources {
		for _, s := range ms.MediaStreams {
			if strings.EqualFold(s.Type, "Audio") {
				return s, true
			}
		}
	}
	return MediaStream{}, false
}

func (item Item) FirstMediaSource() (MediaSource, bool) {
	if len(item.MediaSources) > 0 {
		return item.MediaSources[0], true
	}
	return MediaSource{}, false
}

func FromJellyfin(item Item) (library.Track, bool) {
	if item.ID == "" || item.Name == "" {
		return library.Track{}, false
	}
	audioStream, hasAudio := item.AudioStream()
	mediaSource, hasMedia := item.FirstMediaSource()
	if !hasAudio && !hasMedia && item.Container == "" {
		return library.Track{}, false
	}

	container := strings.ToLower(item.Container)
	if container == "" {
		container = strings.ToLower(mediaSource.Container)
	}
	codec := strings.ToLower(audioStream.Codec)
	if codec == "" {
		codec = container
	}
	format := AudioFormat(codec, container)

	artist := ""
	if len(item.Artists) > 0 {
		artist = strings.Join(item.Artists, ", ")
	} else if item.AlbumArtist != "" {
		artist = item.AlbumArtist
	}

	bitrate := audioStream.BitRate
	if bitrate == 0 {
		bitrate = mediaSource.Bitrate
	}
	bitrateKbps := (bitrate + 500) / 1000

	durationSec := int((item.RunTimeTicks + 5_000_000) / 10_000_000)

	thumb := ""
	if item.ImageTags != nil && item.ImageTags["Primary"] != "" {
		thumb = ArtPath(item.ID, item.ImageTags["Primary"])
	} else if item.AlbumPrimaryImageTag != "" && item.AlbumID != "" {
		thumb = ArtPath(item.AlbumID, item.AlbumPrimaryImageTag)
	}

	return library.Track{
		ID:          item.ID,
		Title:       item.Name,
		Artist:      artist,
		AlbumArtist: item.AlbumArtist,
		Album:       item.Album,
		DurationSec: durationSec,
		Codec:       format,
		Container:   container,
		BitrateKbps: bitrateKbps,
		PartKey:     StreamPath(item.ID),
		Thumb:       thumb,
	}, true
}

func StreamPath(id string) string {
	return "/Audio/" + url.PathEscape(id) + "/stream?static=true"
}

func ArtPath(id, tag string) string {
	p := "/Items/" + url.PathEscape(id) + "/Images/Primary?fillWidth=600&fillHeight=600&quality=90"
	if tag != "" {
		p += "&tag=" + url.QueryEscape(tag)
	}
	return p
}

func AudioFormat(codec, container string) string {
	codec = strings.ToLower(codec)
	container = strings.ToLower(container)
	if codec == "pcm" && (container == "wav" || container == "aiff") {
		return container
	}
	if codec == "" {
		return container
	}
	return codec
}

func Lossless(format string) bool {
	switch strings.ToLower(format) {
	case "flac", "alac", "wav", "aiff":
		return true
	}
	return false
}

func Quality(format string, kbps int, sampleRate, bitDepth int) string {
	if !Lossless(format) {
		if kbps == 0 {
			return format
		}
		return fmt.Sprintf("%dkbps", kbps)
	}
	out := "lossless"
	if bitDepth > 0 {
		out += fmt.Sprintf(" %d-bit", bitDepth)
	}
	if sampleRate > 0 {
		out += " " + strconv.FormatFloat(float64(sampleRate)/1000, 'f', -1, 64) + "kHz"
	}
	return out
}
