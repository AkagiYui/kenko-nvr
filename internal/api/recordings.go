package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

func (s *Server) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := database.RecordingFilter{
		CameraID: q.Get("cameraId"),
		Limit:    atoiDefault(q.Get("limit"), 200),
		Offset:   atoiDefault(q.Get("offset"), 0),
	}
	if v := q.Get("from"); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.From = time.UnixMilli(ms)
		}
	}
	if v := q.Get("to"); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.To = time.UnixMilli(ms)
		}
	}

	recs, err := s.db.Recordings.List(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if recs == nil {
		recs = []database.Recording{}
	}
	writeJSON(w, http.StatusOK, recs)
}

func (s *Server) handleGetRecording(w http.ResponseWriter, r *http.Request) {
	rec, err := s.db.Recordings.Get(chi.URLParam(r, "id"))
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleDeleteRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := s.db.Recordings.Get(id)
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}
	abs := filepath.Join(s.cfg.Storage.RecordingsDir, filepath.FromSlash(rec.Path))
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.db.Recordings.Delete(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRecordingFile streams a recording with HTTP range support so the
// browser can scrub through it.
func (s *Server) handleRecordingFile(w http.ResponseWriter, r *http.Request) {
	rec, err := s.db.Recordings.Get(chi.URLParam(r, "id"))
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}
	abs := filepath.Join(s.cfg.Storage.RecordingsDir, filepath.FromSlash(rec.Path))
	f, err := os.Open(abs)
	if err != nil {
		writeErr(w, http.StatusNotFound, "file not found (may have been uploaded and removed)")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(rec.Path)+"\"")
	}
	http.ServeContent(w, r, filepath.Base(rec.Path), info.ModTime(), f)
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
