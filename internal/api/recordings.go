package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/storage"
)

// recordingArchive opens an archived recording from object storage for playback.
type recordingArchive interface {
	Open(ctx context.Context, key string) (*storage.Object, error)
}

// errS3Disabled is returned by the archive when S3 is not configured/enabled, so
// the caller falls through to a local 404 rather than logging an error.
var errS3Disabled = errors.New("s3 storage disabled")

// s3Archive opens objects from the live S3 settings. It rebuilds the client from
// current config on each open so edits in the settings UI take effect without a
// restart.
type s3Archive struct{ settings *database.SettingsStore }

func (a s3Archive) Open(ctx context.Context, key string) (*storage.Object, error) {
	cfg, err := a.settings.S3()
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errS3Disabled
	}
	up, err := storage.NewUploader(cfg)
	if err != nil {
		return nil, err
	}
	return up.Open(ctx, key)
}

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
// browser can scrub through it. The local file is served when present; once it
// has been deleted (by retention or after upload) but is preserved on S3, the
// clip is streamed back from the bucket through the NVR — so clients with no
// direct internet/S3 access can still watch archived footage.
func (s *Server) handleRecordingFile(w http.ResponseWriter, r *http.Request) {
	rec, err := s.db.Recordings.Get(chi.URLParam(r, "id"))
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}
	abs := filepath.Join(s.cfg.Storage.RecordingsDir, filepath.FromSlash(rec.Path))
	f, err := os.Open(abs)
	if err == nil {
		defer f.Close()
		info, statErr := f.Stat()
		if statErr != nil {
			writeErr(w, http.StatusInternalServerError, statErr.Error())
			return
		}
		s.setRecordingHeaders(w, r, rec)
		http.ServeContent(w, r, filepath.Base(rec.Path), info.ModTime(), f)
		return
	}
	if !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Local file is gone: fall back to S3 if the clip was archived there.
	if s.serveRecordingFromS3(w, r, rec) {
		return
	}
	writeErr(w, http.StatusNotFound, "recording not available locally or on S3")
}

// serveRecordingFromS3 streams the recording from the configured S3 bucket back
// through the NVR. It reports whether it served the file; on any miss (not
// uploaded, S3 disabled, or an error reaching the bucket) it returns false
// without writing a response body so the caller can fall through to 404.
func (s *Server) serveRecordingFromS3(w http.ResponseWriter, r *http.Request, rec database.Recording) bool {
	if s.archive == nil || !rec.Uploaded || rec.S3Key == "" {
		return false
	}
	obj, err := s.archive.Open(r.Context(), rec.S3Key)
	if err != nil {
		if !errors.Is(err, errS3Disabled) {
			s.log.Error("s3 playback: open object failed", "key", rec.S3Key, "err", err)
		}
		return false
	}
	defer obj.Close()
	s.setRecordingHeaders(w, r, rec)
	http.ServeContent(w, r, filepath.Base(rec.Path), obj.ModTime, obj.Body)
	return true
}

// setRecordingHeaders sets the content type and, for ?download requests, the
// attachment disposition shared by local and S3 playback.
func (s *Server) setRecordingHeaders(w http.ResponseWriter, r *http.Request, rec database.Recording) {
	w.Header().Set("Content-Type", "video/mp4")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(rec.Path)+"\"")
	}
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
