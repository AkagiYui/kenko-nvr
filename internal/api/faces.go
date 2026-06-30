package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/face"
)

// handleListPersons returns discovered identities, most-recently-seen first.
func (s *Server) handleListPersons(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	persons, err := s.db.Persons.List(database.PersonFilter{
		NamedOnly: q.Get("named") != "",
		Limit:     atoiDefault(q.Get("limit"), 500),
		Offset:    atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if persons == nil {
		persons = []database.Person{}
	}
	writeJSON(w, http.StatusOK, persons)
}

// personDetail is a person plus its appearances (tracks) and, optionally, the
// recordings those appearances fall in (for jump-to-playback metadata).
type personDetail struct {
	database.Person
	Appearances []database.FaceTrack `json:"appearances"`
	Recordings  []database.Recording `json:"recordings,omitempty"`
}

// handleGetPerson returns a person with its appearances (face tracks). Pass
// withRecordings=1 to also embed the distinct recordings referenced, so the UI
// can show clip metadata and seek to the appearance offset.
func (s *Server) handleGetPerson(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	person, err := s.db.Persons.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "person not found")
		return
	}
	tracks, err := s.db.FaceTracks.List(database.FaceTrackFilter{PersonID: id, Limit: 2000})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tracks == nil {
		tracks = []database.FaceTrack{}
	}
	out := personDetail{Person: person, Appearances: tracks}
	if r.URL.Query().Get("withRecordings") != "" {
		out.Recordings = s.recordingsForTracks(tracks)
	}
	writeJSON(w, http.StatusOK, out)
}

// recordingsForTracks loads the distinct recordings referenced by a set of
// tracks, in one pass.
func (s *Server) recordingsForTracks(tracks []database.FaceTrack) []database.Recording {
	seen := make(map[string]bool)
	out := []database.Recording{}
	for _, t := range tracks {
		if t.RecordingID == "" || seen[t.RecordingID] {
			continue
		}
		seen[t.RecordingID] = true
		if rec, err := s.db.Recordings.Get(t.RecordingID); err == nil {
			out = append(out, rec)
		}
	}
	return out
}

// handleListFaces returns face detections, filtered by person / recording /
// track. Embeddings are never included (json:"-").
func (s *Server) handleListFaces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := database.FaceFilter{
		PersonID:    q.Get("personId"),
		RecordingID: q.Get("recordingId"),
		CameraID:    q.Get("cameraId"),
		TrackID:     q.Get("trackId"),
		Limit:       atoiDefault(q.Get("limit"), 500),
		Offset:      atoiDefault(q.Get("offset"), 0),
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
	faces, err := s.db.Faces.List(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if faces == nil {
		faces = []database.Face{}
	}
	writeJSON(w, http.StatusOK, faces)
}

// faceStatus reports the feature/sidecar/queue state for the settings UI.
type faceStatus struct {
	Enabled     bool                          `json:"enabled"`
	SidecarOK   bool                          `json:"sidecarOk"`
	Sidecar     *face.Health                  `json:"sidecar,omitempty"`
	SidecarErr  string                        `json:"sidecarErr,omitempty"`
	Jobs        map[database.FaceJobState]int `json:"jobs"`
	PersonCount int                           `json:"personCount"`
}

// handleFaceStatus returns whether face recognition is enabled, the sidecar
// health, and the job-queue counts.
func (s *Server) handleFaceStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.db.Settings.Face()
	st := faceStatus{Enabled: cfg.Enabled, Jobs: map[database.FaceJobState]int{}}
	if counts, err := s.db.FaceJobs.Counts(); err == nil {
		st.Jobs = counts
	}
	if persons, err := s.db.Persons.List(database.PersonFilter{Limit: 1_000_000}); err == nil {
		st.PersonCount = len(persons)
	}
	if cfg.SidecarURL != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		if h, err := face.NewClient(cfg.SidecarURL).Health(ctx); err == nil {
			st.SidecarOK = h.Status == "ok"
			st.Sidecar = &h
		} else {
			st.SidecarErr = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// handleGetFaceSettings returns the face-recognition config.
func (s *Server) handleGetFaceSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.db.Settings.Face()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handleSetFaceSettings stores the face-recognition config (after clamping the
// numeric fields to safe ranges).
func (s *Server) handleSetFaceSettings(w http.ResponseWriter, r *http.Request) {
	var cfg database.FaceConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	cfg = sanitizeFaceConfig(cfg)
	if err := s.db.Settings.SetFace(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func sanitizeFaceConfig(c database.FaceConfig) database.FaceConfig {
	d := database.DefaultFaceConfig()
	if c.SidecarURL == "" {
		c.SidecarURL = d.SidecarURL
	}
	if c.SampleFPS <= 0 || c.SampleFPS > 30 {
		c.SampleFPS = d.SampleFPS
	}
	if c.MaxFramesPerJob <= 0 {
		c.MaxFramesPerJob = d.MaxFramesPerJob
	}
	if c.BatchSize <= 0 || c.BatchSize > 128 {
		c.BatchSize = d.BatchSize
	}
	if c.MinFaceSize < 0 {
		c.MinFaceSize = d.MinFaceSize
	}
	if c.DetThreshold <= 0 || c.DetThreshold > 1 {
		c.DetThreshold = d.DetThreshold
	}
	if c.MatchThreshold <= 0 || c.MatchThreshold > 1 {
		c.MatchThreshold = d.MatchThreshold
	}
	if c.ReviewThreshold <= 0 || c.ReviewThreshold > c.MatchThreshold {
		c.ReviewThreshold = d.ReviewThreshold
	}
	if c.RealtimeFPS <= 0 || c.RealtimeFPS > 10 {
		c.RealtimeFPS = d.RealtimeFPS
	}
	return c
}

// scanRequest selects which completed recordings to (re)enqueue for analysis.
type scanRequest struct {
	CameraID string `json:"cameraId"`
	From     int64  `json:"from"` // epoch ms, optional
	To       int64  `json:"to"`   // epoch ms, optional
	Limit    int    `json:"limit"`
}

// handleFaceScan enqueues face-analysis jobs for existing recordings (backfill /
// reprocess). The worker only runs them when the feature is enabled.
func (s *Server) handleFaceScan(w http.ResponseWriter, r *http.Request) {
	var req scanRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	f := database.RecordingFilter{CameraID: req.CameraID, Limit: req.Limit}
	if f.Limit <= 0 {
		f.Limit = 1000
	}
	if req.From > 0 {
		f.From = time.UnixMilli(req.From)
	}
	if req.To > 0 {
		f.To = time.UnixMilli(req.To)
	}
	recs, err := s.db.Recordings.List(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	enqueued := 0
	for _, rec := range recs {
		if !rec.Complete {
			continue
		}
		if err := s.db.FaceJobs.Enqueue(rec.ID); err == nil {
			enqueued++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"enqueued": enqueued, "matched": len(recs)})
}
