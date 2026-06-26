package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// handleListEvents returns detected events (motion) for the timeline UI and the
// dashboard events feed.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := database.EventFilter{
		CameraID: q.Get("cameraId"),
		Type:     database.EventType(q.Get("type")),
		Limit:    atoiDefault(q.Get("limit"), 200),
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
	events, err := s.db.Events.List(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []database.Event{}
	}
	writeJSON(w, http.StatusOK, events)
}
