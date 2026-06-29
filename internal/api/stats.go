package api

import "net/http"

// handleStats returns system-wide camera counts and live ingest traffic. Any
// authenticated user may read it (the sidebar polls it for the traffic widget).
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.SystemStats())
}
