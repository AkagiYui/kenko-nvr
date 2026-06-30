package api

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
)

// hlsFileRe restricts the files servable from a transcode session to its
// playlist and segments (no path traversal).
var hlsFileRe = regexp.MustCompile(`^(index\.m3u8|seg\d+\.ts)$`)

// handleRecordingHLSStart starts (or reuses) an on-demand H.264 HLS transcode of
// a recording and returns the playlist URL for the player to load. ?from=<sec>
// starts transcoding at that offset (used for jump-to-appearance playback).
func (s *Server) handleRecordingHLSStart(w http.ResponseWriter, r *http.Request) {
	rec, err := s.db.Recordings.Get(chi.URLParam(r, "id"))
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}
	if rec.LocalRemoved {
		// The transcoder reads local files; S3-only clips still play via the
		// original-MP4 endpoint (which streams from the bucket).
		writeErr(w, http.StatusConflict, "HLS playback not available for cloud-only recordings; use the original format")
		return
	}
	abs := filepath.Join(s.cfg.Storage.RecordingsDir, filepath.FromSlash(rec.Path))
	if _, err := os.Stat(abs); err != nil {
		writeErr(w, http.StatusNotFound, "recording file not found")
		return
	}
	from := atoiDefault(r.URL.Query().Get("from"), 0)
	if from < 0 {
		from = 0
	}
	sid, err := s.rechls.Ensure(r.Context(), rec.ID, abs, from)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"playlist": "/api/rechls/" + sid + "/index.m3u8"})
}

// handleHLSFile serves a transcode session's playlist or segments. Segment URLs
// are relative in the playlist, so they resolve under the same session path; the
// player carries the bearer token, so this lives behind the media middleware.
func (s *Server) handleHLSFile(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sid")
	name := chi.URLParam(r, "file")
	if !hlsFileRe.MatchString(name) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	path, ok := s.rechls.Segment(sid, name)
	if !ok {
		http.Error(w, "playback session expired", http.StatusNotFound)
		return
	}
	if strings.HasSuffix(name, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	} else {
		w.Header().Set("Content-Type", "video/mp2t")
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, path)
}
