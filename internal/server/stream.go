package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/rairulyle/bitchord-selfhosted-addon/internal/jellyfin"
	"github.com/rairulyle/bitchord-selfhosted-addon/internal/library"
	"github.com/rairulyle/bitchord-selfhosted-addon/internal/plex"
)

var idPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

func trackID(r *http.Request) (string, bool) {
	id := r.PathValue("id")
	return id, idPattern.MatchString(id)
}

func (s *server) trackID(r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if id == "" {
		return "", false
	}
	b := s.backend()
	if b != nil {
		return id, b.ValidID(id)
	}
	return id, idPattern.MatchString(id)
}

func (s *server) stream(w http.ResponseWriter, r *http.Request) {
	id, ok := s.trackID(r)
	if !ok {
		quiet404(w, r)
		return
	}
	info, status := s.lookupStream(r, id)
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	s.Log.Info("stream", "id", id, "track", info.Label, "quality", info.Quality, "format", info.Format)
	writeJSON(w, streamJSONFromInfo(s.base(), info))
}

func (s *server) lookupStream(r *http.Request, id string) (library.StreamInfo, int) {
	ctx, cancel := context.WithTimeout(r.Context(), s.lookupTimeout)
	defer cancel()
	b := s.backend()
	if b == nil {
		return library.StreamInfo{}, http.StatusBadGateway
	}
	info, err := b.StreamInfo(ctx, id)
	switch {
	case err == nil:
		return info, http.StatusOK
	case errors.Is(err, plex.ErrNotFound) || errors.Is(err, jellyfin.ErrNotFound):
		return library.StreamInfo{}, http.StatusNotFound
	}
	s.logFailure(r, err)
	return library.StreamInfo{}, http.StatusBadGateway
}

func (s *server) lookup(r *http.Request, id string) (plex.Track, int) {
	if s.Plex == nil {
		return plex.Track{}, http.StatusNotFound
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.lookupTimeout)
	defer cancel()
	item, err := s.Plex.Track(ctx, id)
	switch {
	case err == nil:
		return item, http.StatusOK
	case errors.Is(err, plex.ErrNotFound):
		return plex.Track{}, http.StatusNotFound
	}
	s.logPlexFailure(r, err)
	return plex.Track{}, http.StatusBadGateway
}

func (s *server) logPlexFailure(r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	if errors.Is(err, plex.ErrUnauthorized) {
		s.Log.Error("plex rejected the token, check PLEX_TOKEN")
		return
	}
	s.Log.Error("plex request failed", "error", err.Error())
}

func (s *server) logFailure(r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	b := s.backend()
	if b != nil {
		s.Log.Error(b.FormatError(err))
		return
	}
	s.Log.Error("backend request failed", "error", err.Error())
}
