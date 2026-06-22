package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/manager"
)

// cameraResponse merges stored config with live status.
type cameraResponse struct {
	database.Camera
	Status manager.CameraStatus `json:"status"`
}

func (s *Server) handleListCameras(w http.ResponseWriter, r *http.Request) {
	cams, err := s.db.Cameras.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]cameraResponse, 0, len(cams))
	for _, c := range cams {
		c.Password = ""
		c.OnvifPassword = ""
		out = append(out, cameraResponse{Camera: c, Status: s.mgr.Status(c.ID)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cam, err := s.db.Cameras.Get(id)
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}
	cam.Password = ""
	cam.OnvifPassword = ""
	writeJSON(w, http.StatusOK, cameraResponse{Camera: cam, Status: s.mgr.Status(id)})
}

func (s *Server) handleCreateCamera(w http.ResponseWriter, r *http.Request) {
	var cam database.Camera
	if err := decodeJSON(r, &cam); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := validateCamera(&cam); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cam.ID = uuid.NewString()
	if err := s.db.Cameras.Create(cam); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.mgr.ApplyCamera(cam)
	cam.Password = ""
	cam.OnvifPassword = ""
	writeJSON(w, http.StatusCreated, cam)
}

func (s *Server) handleUpdateCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.db.Cameras.Get(id)
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}

	var cam database.Camera
	if err := decodeJSON(r, &cam); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	cam.ID = id
	// Preserve secrets when the client sends them blank (it never receives them).
	if cam.Password == "" {
		cam.Password = existing.Password
	}
	if cam.OnvifPassword == "" {
		cam.OnvifPassword = existing.OnvifPassword
	}
	if err := validateCamera(&cam); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.db.Cameras.Update(cam); err != nil {
		s.notFoundOr500(w, err)
		return
	}
	s.mgr.ApplyCamera(cam)
	cam.Password = ""
	cam.OnvifPassword = ""
	writeJSON(w, http.StatusOK, cam)
}

func (s *Server) handleDeleteCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mgr.RemoveCamera(id)
	if err := s.db.Cameras.Delete(id); err != nil {
		s.notFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCameraStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Status(chi.URLParam(r, "id")))
}

func (s *Server) handleAllStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.AllStatus())
}

func validateCamera(c *database.Camera) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return errors.New("name is required")
	}
	switch c.SourceType {
	case database.SourceRTSP:
		if strings.TrimSpace(c.URL) == "" {
			return errors.New("rtsp url is required")
		}
	case database.SourceRTMP:
		// URL not needed: the camera publishes to rtmp://host/live/<id>.
	case database.SourceONVIF:
		if strings.TrimSpace(c.OnvifXAddr) == "" {
			return errors.New("onvif device address is required")
		}
		// An ONVIF source is also the control endpoint, so PTZ is available.
		c.OnvifEnabled = true
	default:
		return errors.New("sourceType must be 'rtsp', 'rtmp' or 'onvif'")
	}
	return nil
}

func (s *Server) notFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, database.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}
