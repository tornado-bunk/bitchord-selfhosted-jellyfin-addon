package server

import (
	"io"
	"net/http"
	"time"

	"github.com/rairulyle/bitchord-selfhosted-addon/internal/plex"
)

var (
	forwardedRequestHeaders  = []string{"Range", "If-Range"}
	forwardedResponseHeaders = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"}
)

func (s *server) file(w http.ResponseWriter, r *http.Request) {
	track, status := s.resolve(r)
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	header := http.Header{}
	for _, name := range forwardedRequestHeaders {
		if value := r.Header.Get(name); value != "" {
			header.Set(name, value)
		}
	}
	started := time.Now()
	b := s.backend()
	if b == nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	id := r.PathValue("id")
	upstream, err := b.OpenFile(r.Context(), r.Method, id, track.partKey, header)
	if err != nil {
		s.logFailure(r, err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	sent := s.transfer(w, r, upstream, "")
	switch {
	case sent.ended == "":
	case r.Method == http.MethodHead:
		s.Log.Debug("probe", "id", id, "track", track.label, "status", sent.status)
	default:
		s.Log.Info("play", "id", id, "track", track.label, "range", r.Header.Get("Range"),
			"status", sent.status, "bytes", sent.bytes, "ended", sent.ended, "took", time.Since(started).String())
	}
}

func (s *server) art(w http.ResponseWriter, r *http.Request) {
	track, status := s.resolve(r)
	if status == http.StatusOK && track.thumb == "" {
		status = http.StatusNotFound
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	b := s.backend()
	if b == nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	id := r.PathValue("id")
	upstream, err := b.OpenArt(r.Context(), id, track.thumb)
	if err != nil {
		s.logFailure(r, err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	s.transfer(w, r, upstream, "public, max-age=86400")
}

type resolved struct{ partKey, thumb, label string }

type sent struct {
	status int
	bytes  int64
	ended  string
}

type clientWriter struct {
	io.Writer
	failed bool
}

func (c *clientWriter) Write(p []byte) (int, error) {
	n, err := c.Writer.Write(p)
	if err != nil {
		c.failed = true
	}
	return n, err
}

func (s *server) resolve(r *http.Request) (resolved, int) {
	id, ok := s.trackID(r)
	if !ok {
		return resolved{}, http.StatusNotFound
	}
	if track, found := s.Library.Get(id); found {
		return resolved{track.PartKey, track.Thumb, label(track)}, http.StatusOK
	}
	info, status := s.lookupStream(r, id)
	if status != http.StatusOK {
		return resolved{}, status
	}
	return resolved{info.PartKey, info.Thumb, info.Label}, http.StatusOK
}

// Plex answers 500 to a direct-play request for a track it never finished
// analysing (no media bitrate), yet serves the same part as a download.
func (s *server) pipe(w http.ResponseWriter, r *http.Request, method, path string, header http.Header, cacheControl, retryPath string) sent {
	if s.Plex == nil {
		w.WriteHeader(http.StatusBadGateway)
		return sent{}
	}
	upstream, err := s.Plex.Open(r.Context(), method, path, header)
	if err == nil && upstream.StatusCode == http.StatusInternalServerError && retryPath != "" {
		upstream.Body.Close()
		s.Log.Debug("plex refused direct play, retrying as a download", "id", r.PathValue("id"))
		upstream, err = s.Plex.Open(r.Context(), method, retryPath, header)
	}
	if err != nil {
		s.logPlexFailure(r, err)
		w.WriteHeader(http.StatusBadGateway)
		return sent{}
	}
	return s.transfer(w, r, upstream, cacheControl)
}

func (s *server) transfer(w http.ResponseWriter, r *http.Request, upstream *http.Response, cacheControl string) sent {
	defer upstream.Body.Close()
	switch upstream.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
	case http.StatusNotFound:
		w.WriteHeader(http.StatusNotFound)
		return sent{}
	case http.StatusUnauthorized:
		s.logFailure(r, plex.ErrUnauthorized)
		w.WriteHeader(http.StatusBadGateway)
		return sent{}
	default:
		s.Log.Error("upstream answered the byte request badly", "status", upstream.StatusCode)
		w.WriteHeader(http.StatusBadGateway)
		return sent{}
	}
	for _, name := range forwardedResponseHeaders {
		if value := upstream.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	if cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	}
	w.WriteHeader(upstream.StatusCode)
	out := sent{status: upstream.StatusCode, ended: "complete"}
	if r.Method == http.MethodHead {
		return out
	}
	client := &clientWriter{Writer: w}
	var err error
	out.bytes, err = io.Copy(client, upstream.Body)
	switch {
	case err == nil:
	case client.failed || r.Context().Err() != nil:
		out.ended = "client left"
	default:
		out.ended = "upstream error"
		s.Log.Debug("stream ended early", "error", err.Error())
	}
	return out
}
