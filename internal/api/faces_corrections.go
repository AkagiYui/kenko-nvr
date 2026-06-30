package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/face"
)

// faceExemplars mirrors the gallery's per-person exemplar cap; corrections
// rebuild a person's exemplars to this size.
const faceExemplars = 8

func newPersonID() string { return uuid.NewString() }

// refreshPerson recomputes a person's denormalised counts and exemplar set after
// a membership change, deleting the person if it has no faces left.
func (s *Server) refreshPerson(id string) {
	if id == "" {
		return
	}
	n, err := s.db.Persons.Recount(id)
	if err != nil {
		return
	}
	if n == 0 {
		_ = s.db.Persons.Delete(id)
		return
	}
	_ = s.db.Faces.RebuildExemplars(id, faceExemplars)
}

// patchPersonReq is a partial update; nil fields are left unchanged.
type patchPersonReq struct {
	Name        *string `json:"name"`
	Notes       *string `json:"notes"`
	CoverFaceID *string `json:"coverFaceId"`
}

// handlePatchPerson renames / annotates / re-covers a person.
func (s *Server) handlePatchPerson(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.db.Persons.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "person not found")
		return
	}
	var req patchPersonReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	name, notes, cover := p.Name, p.Notes, p.CoverFaceID
	if req.Name != nil {
		name = *req.Name
	}
	if req.Notes != nil {
		notes = *req.Notes
	}
	if req.CoverFaceID != nil {
		cover = *req.CoverFaceID
	}
	if err := s.db.Persons.UpdateMeta(id, name, notes, cover); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, _ := s.db.Persons.Get(id)
	writeJSON(w, http.StatusOK, updated)
}

// handleDeletePerson removes a person and detaches its faces/tracks (they become
// unassigned and re-clusterable).
func (s *Server) handleDeletePerson(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_ = s.db.FaceTracks.ReassignPerson(id, "")
	_ = s.db.Faces.ReassignPerson(id, "")
	if err := s.db.Persons.Delete(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type mergeReq struct {
	SourceIDs []string `json:"sourceIds"`
	TargetID  string   `json:"targetId"`
}

// handleMergePersons folds one or more source persons into a target (same
// person). Moved tracks are confirmed so re-clustering keeps them together.
func (s *Server) handleMergePersons(w http.ResponseWriter, r *http.Request) {
	var req mergeReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.TargetID == "" || len(req.SourceIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "targetId and sourceIds required")
		return
	}
	if _, err := s.db.Persons.Get(req.TargetID); err != nil {
		writeErr(w, http.StatusNotFound, "target person not found")
		return
	}
	for _, src := range req.SourceIDs {
		if src == "" || src == req.TargetID {
			continue
		}
		srcTracks, _ := s.db.FaceTracks.List(database.FaceTrackFilter{PersonID: src, Limit: 100000})
		_ = s.db.FaceTracks.ReassignPerson(src, req.TargetID)
		_ = s.db.Faces.ReassignPerson(src, req.TargetID)
		for _, t := range srcTracks {
			_ = s.db.FaceTracks.SetConfirmed(t.ID, true)
		}
		_ = s.db.Persons.Delete(src)
	}
	s.refreshPerson(req.TargetID)
	updated, _ := s.db.Persons.Get(req.TargetID)
	writeJSON(w, http.StatusOK, updated)
}

type splitReq struct {
	TrackIDs []string `json:"trackIds"`
}

// handleSplitPerson moves the given tracks of a person out into a new person
// (correcting a wrong merge). The moved tracks are confirmed.
func (s *Server) handleSplitPerson(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.db.Persons.Get(id); err != nil {
		writeErr(w, http.StatusNotFound, "person not found")
		return
	}
	var req splitReq
	if err := decodeJSON(r, &req); err != nil || len(req.TrackIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "trackIds required")
		return
	}
	newID := newPersonID()
	if err := s.db.Persons.Create(database.Person{ID: newID}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, trackID := range req.TrackIDs {
		t, err := s.db.FaceTracks.Get(trackID)
		if err != nil || t.PersonID != id {
			continue
		}
		_ = s.db.FaceTracks.UpdatePerson(trackID, newID)
		_ = s.db.FaceTracks.SetConfirmed(trackID, true)
		_ = s.db.Faces.SetPersonByTrack(trackID, newID)
	}
	s.refreshPerson(id)
	s.refreshPerson(newID)
	out, _ := s.db.Persons.Get(newID)
	writeJSON(w, http.StatusOK, out)
}

type assignTrackReq struct {
	PersonID  string `json:"personId"`
	NewPerson bool   `json:"newPerson"`
}

// handleAssignTrack moves one track to a person (existing or new) and confirms
// it, so the assignment is locked against re-clustering.
func (s *Server) handleAssignTrack(w http.ResponseWriter, r *http.Request) {
	trackID := chi.URLParam(r, "id")
	t, err := s.db.FaceTracks.Get(trackID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "track not found")
		return
	}
	var req assignTrackReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	target := req.PersonID
	switch {
	case req.NewPerson:
		target = newPersonID()
		if err := s.db.Persons.Create(database.Person{ID: target}); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	case target == "":
		writeErr(w, http.StatusBadRequest, "personId or newPerson required")
		return
	default:
		if _, err := s.db.Persons.Get(target); err != nil {
			writeErr(w, http.StatusNotFound, "person not found")
			return
		}
	}
	old := t.PersonID
	_ = s.db.FaceTracks.UpdatePerson(trackID, target)
	_ = s.db.FaceTracks.SetConfirmed(trackID, true)
	_ = s.db.Faces.SetPersonByTrack(trackID, target)
	if old != target {
		s.refreshPerson(old)
	}
	s.refreshPerson(target)
	out, _ := s.db.Persons.Get(target)
	writeJSON(w, http.StatusOK, out)
}

// handleConfirmTrack locks a track's current assignment.
func (s *Server) handleConfirmTrack(w http.ResponseWriter, r *http.Request) {
	if err := s.db.FaceTracks.SetConfirmed(chi.URLParam(r, "id"), true); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type ignoreReq struct {
	Ignored bool `json:"ignored"`
}

// handleIgnoreFace flags a face as a false positive (or clears the flag).
func (s *Server) handleIgnoreFace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	f, err := s.db.Faces.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "face not found")
		return
	}
	var req ignoreReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.db.Faces.SetIgnored(id, req.Ignored); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.refreshPerson(f.PersonID)
	w.WriteHeader(http.StatusNoContent)
}

type linkReq struct {
	Kind   string `json:"kind"`
	ATrack string `json:"aTrack"`
	BTrack string `json:"bTrack"`
}

// handleCreateLink records a must/cannot clustering constraint between two tracks.
func (s *Server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	var req linkReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	kind := database.LinkKind(req.Kind)
	if kind != database.LinkMust && kind != database.LinkCannot {
		writeErr(w, http.StatusBadRequest, "kind must be 'must' or 'cannot'")
		return
	}
	if req.ATrack == "" || req.BTrack == "" || req.ATrack == req.BTrack {
		writeErr(w, http.StatusBadRequest, "two distinct track ids required")
		return
	}
	id, err := s.db.PersonLinks.Create(database.PersonLink{Kind: kind, ATrack: req.ATrack, BTrack: req.BTrack})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// handleDeleteLink removes a clustering constraint.
func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PersonLinks.Delete(chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRecluster runs a global re-clustering pass now and returns its summary.
func (s *Server) handleRecluster(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.db.Settings.Face()
	c := &face.Clusterer{DB: s.db, Log: s.log}
	res, err := c.Recluster(r.Context(), cfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
