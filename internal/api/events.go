package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// handleListEvents returns detected events (motion) for the timeline UI and the
// dashboard events feed. cameraId and type may each be repeated (or comma-
// separated) to filter by several cameras / event types at once.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := database.EventFilter{
		CameraIDs: multiParam(q, "cameraId"),
		Types:     eventTypes(multiParam(q, "type")),
		Limit:     atoiDefault(q.Get("limit"), 200),
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

	// The events page asks for the recording clips overlapping each event so it
	// can offer direct playback; the timeline (no flag) takes the plain list.
	if q.Get("withRecordings") != "" {
		writeJSON(w, http.StatusOK, s.eventsWithRecordings(events, f.CameraIDs))
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// multiParam collects a repeatable query parameter, also splitting each value on
// commas, with surrounding whitespace and empty entries removed.
func multiParam(q url.Values, key string) []string {
	var out []string
	for _, v := range q[key] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// eventTypes converts raw strings into EventType values.
func eventTypes(vals []string) []database.EventType {
	if len(vals) == 0 {
		return nil
	}
	out := make([]database.EventType, len(vals))
	for i, v := range vals {
		out[i] = database.EventType(v)
	}
	return out
}

// eventResponse is an event plus the recording clips that cover its time span.
type eventResponse struct {
	database.Event
	Recordings []database.Recording `json:"recordings"`
}

// eventsWithRecordings attaches to each event the recordings whose span overlaps
// it. All candidate recordings are fetched once (over the events' bounding
// window) and matched in memory, so the whole page costs two queries.
func (s *Server) eventsWithRecordings(events []database.Event, cameraIDs []string) []eventResponse {
	out := make([]eventResponse, len(events))
	if len(events) == 0 {
		return out
	}

	minStart, maxEnd := eventEnd(events[0]), eventEnd(events[0])
	for _, e := range events {
		if st := e.StartTime.Time; st.Before(minStart) {
			minStart = st
		}
		if en := eventEnd(e); en.After(maxEnd) {
			maxEnd = en
		}
	}

	recs, err := s.db.Recordings.ListOverlapping(cameraIDs, minStart, maxEnd)
	if err != nil && s.log != nil {
		s.log.Warn("listing event recordings", "err", err)
	}

	for i, e := range events {
		er := eventResponse{Event: e, Recordings: []database.Recording{}}
		evStart, evEnd := e.StartTime.Time, eventEnd(e)
		for _, rec := range recs {
			if rec.CameraID == e.CameraID && recordingCovers(rec, evStart, evEnd) {
				er.Recordings = append(er.Recordings, rec)
			}
		}
		out[i] = er
	}
	return out
}

// eventEnd is an event's end time, falling back to now while it is in progress.
func eventEnd(e database.Event) time.Time {
	if e.EndTime.Time.IsZero() {
		return time.Now()
	}
	return e.EndTime.Time
}

// recordingCovers reports whether a recording's span intersects [evStart, evEnd].
// An in-progress recording is treated as running up to now.
func recordingCovers(rec database.Recording, evStart, evEnd time.Time) bool {
	rs := rec.StartTime.Time
	re := rec.EndTime.Time
	if re.IsZero() || !rec.Complete {
		re = time.Now()
	}
	return !rs.After(evEnd) && !re.Before(evStart)
}
