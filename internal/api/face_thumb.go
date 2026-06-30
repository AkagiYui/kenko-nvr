package api

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/storage"
)

// faceCipher returns the thumbnail-decryption cipher from the S3 encryption
// settings, or nil when encryption is off.
func (s *Server) faceCipher() *storage.Cipher {
	cfg, err := s.db.Settings.S3()
	if err != nil {
		return nil
	}
	c, _ := storage.CipherFromS3(cfg)
	return c
}

// serveFaceThumb writes a face's thumbnail JPEG (decrypting if it was stored
// encrypted). Served under the media middleware so <img src="...?token="> works.
func (s *Server) serveFaceThumb(w http.ResponseWriter, faceID string) {
	if faceID == "" {
		http.Error(w, "no thumbnail", http.StatusNotFound)
		return
	}
	f, err := s.db.Faces.Get(faceID)
	if err != nil || f.ThumbPath == "" {
		http.Error(w, "no thumbnail", http.StatusNotFound)
		return
	}
	abs := filepath.Join(s.cfg.Storage.FacesDir, filepath.FromSlash(f.ThumbPath))
	data, err := os.ReadFile(abs)
	if err != nil {
		http.Error(w, "thumbnail missing", http.StatusNotFound)
		return
	}
	if storage.IsEncryptedHeader(data) {
		c := s.faceCipher()
		if c == nil {
			http.Error(w, "thumbnail encrypted but no key configured", http.StatusServiceUnavailable)
			return
		}
		dec, err := c.DecryptAll(data)
		if err != nil {
			http.Error(w, "decrypt failed", http.StatusInternalServerError)
			return
		}
		data = dec
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(data)
}

// handleFaceThumb serves one face detection's cropped thumbnail.
func (s *Server) handleFaceThumb(w http.ResponseWriter, r *http.Request) {
	s.serveFaceThumb(w, chi.URLParam(r, "id"))
}

// handlePersonThumb serves a person's cover thumbnail: the explicit cover face,
// else a high-quality exemplar, else any face that has a thumbnail.
func (s *Server) handlePersonThumb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.db.Persons.Get(id)
	if err != nil {
		http.Error(w, "person not found", http.StatusNotFound)
		return
	}
	faceID := p.CoverFaceID
	if faceID == "" {
		if exs, _ := s.db.Faces.List(database.FaceFilter{PersonID: id, OnlyExemplars: true, Limit: 1}); len(exs) > 0 {
			faceID = exs[0].ID
		} else if any, _ := s.db.Faces.List(database.FaceFilter{PersonID: id, Limit: 1}); len(any) > 0 {
			faceID = any[0].ID
		}
	}
	s.serveFaceThumb(w, faceID)
}
