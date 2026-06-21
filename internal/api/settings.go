package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/storage"
)

func (s *Server) handleHLS(w http.ResponseWriter, r *http.Request) {
	s.mgr.ServeHLS(chi.URLParam(r, "id"), w, r)
}

func (s *Server) handleGetRetention(w http.ResponseWriter, r *http.Request) {
	p, err := s.db.Settings.Retention()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleSetRetention(w http.ResponseWriter, r *http.Request) {
	var p database.RetentionPolicy
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := s.db.Settings.SetRetention(p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleGetS3(w http.ResponseWriter, r *http.Request) {
	c, err := s.db.Settings.S3()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	c.SecretKey = "" // never expose the secret
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleSetS3(w http.ResponseWriter, r *http.Request) {
	existing, _ := s.db.Settings.S3()
	var c database.S3Config
	if err := decodeJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if c.SecretKey == "" {
		c.SecretKey = existing.SecretKey // preserve when left blank
	}
	if err := s.db.Settings.SetS3(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	c.SecretKey = ""
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleTestS3(w http.ResponseWriter, r *http.Request) {
	existing, _ := s.db.Settings.S3()
	var c database.S3Config
	if err := decodeJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if c.SecretKey == "" {
		c.SecretKey = existing.SecretKey
	}
	uploader, err := storage.NewUploader(c)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := uploader.CheckBucket(ctx); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleGetRecordingCfg(w http.ResponseWriter, r *http.Request) {
	c, err := s.db.Settings.Recording()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleSetRecordingCfg(w http.ResponseWriter, r *http.Request) {
	var c database.RecordingConfig
	if err := decodeJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if c.SegmentSeconds <= 0 {
		c.SegmentSeconds = 600
	}
	if err := s.db.Settings.SetRecording(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}
