package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/gb28181"
)

// handleGB28181Info reports the platform parameters a device must register with.
func (s *Server) handleGB28181Info(w http.ResponseWriter, r *http.Request) {
	gb := s.mgr.GB28181()
	if gb == nil {
		writeJSON(w, http.StatusOK, gb28181.Info{Enabled: false})
		return
	}
	writeJSON(w, http.StatusOK, gb.Info())
}

// handleGB28181Devices lists registered GB28181 devices and their channels.
func (s *Server) handleGB28181Devices(w http.ResponseWriter, r *http.Request) {
	gb := s.mgr.GB28181()
	if gb == nil {
		writeJSON(w, http.StatusOK, []gb28181.Device{})
		return
	}
	writeJSON(w, http.StatusOK, gb.Devices())
}

// handleGB28181Refresh re-queries a device's channel catalog.
func (s *Server) handleGB28181Refresh(w http.ResponseWriter, r *http.Request) {
	gb := s.mgr.GB28181()
	if gb == nil {
		writeErr(w, http.StatusBadRequest, "gb28181 is disabled")
		return
	}
	gb.QueryCatalog(chi.URLParam(r, "deviceId"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
