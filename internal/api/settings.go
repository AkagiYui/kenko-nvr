package api

import (
	"context"
	"encoding/json"
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
	c.SecretKey = ""     // never expose the secret
	c.EncryptionKey = "" // never expose the encryption passphrase
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
	if c.EncryptionKey == "" {
		c.EncryptionKey = existing.EncryptionKey // preserve when left blank
	}
	// Keep a stable per-install KDF salt: reuse the existing one, or mint one the
	// first time encryption is enabled. The salt is not secret but must not change
	// once recordings have been encrypted with it.
	c.EncryptionSalt = existing.EncryptionSalt
	if c.EncryptionEnabled && c.EncryptionSalt == "" {
		salt, err := storage.NewSalt()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		c.EncryptionSalt = salt
	}
	if err := s.db.Settings.SetS3(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	c.SecretKey = ""
	c.EncryptionKey = ""
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
	// The connectivity test does not need encryption; preserve the saved key/salt
	// so key derivation in NewUploader does not fail on a blanked passphrase.
	if c.EncryptionKey == "" {
		c.EncryptionKey = existing.EncryptionKey
	}
	if c.EncryptionSalt == "" {
		c.EncryptionSalt = existing.EncryptionSalt
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

func (s *Server) handleGetHA(w http.ResponseWriter, r *http.Request) {
	c, err := s.db.Settings.HomeAssistant()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleSetHA(w http.ResponseWriter, r *http.Request) {
	var c database.HAConfig
	if err := decodeJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if c.DiscoveryPrefix == "" {
		c.DiscoveryPrefix = "homeassistant"
	}
	if c.BaseTopic == "" {
		c.BaseTopic = "kenko-nvr"
	}
	if err := s.db.Settings.SetHomeAssistant(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleGetVideoWall(w http.ResponseWriter, r *http.Request) {
	raw, err := s.db.Settings.VideoWall()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) handleSetVideoWall(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := decodeJSON(r, &raw); err != nil || len(raw) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := s.db.Settings.SetVideoWall(raw); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
