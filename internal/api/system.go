package api

import (
	"net/http"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// handleGetSystem returns the runtime infrastructure config (RTMP / RTSP /
// RTSP-server / WebRTC / GB28181), with the GB28181 device password redacted.
func (s *Server) handleGetSystem(w http.ResponseWriter, r *http.Request) {
	c := s.mgr.SystemConfig()
	c.GB28181.Password = "" // write-only secret
	writeJSON(w, http.StatusOK, c)
}

// handleSetSystem persists a new infrastructure config and applies it live,
// restarting only the servers whose settings changed.
func (s *Server) handleSetSystem(w http.ResponseWriter, r *http.Request) {
	var in database.SystemConfig
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	// A blank GB28181 password means "keep the stored one" (it is never sent down).
	if in.GB28181.Password == "" {
		existing := s.mgr.SystemConfig()
		in.GB28181.Password = existing.GB28181.Password
	}
	if err := s.db.Settings.SetSystem(in); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.mgr.ApplySystemConfig(in)

	out := in
	out.GB28181.Password = ""
	writeJSON(w, http.StatusOK, out)
}
