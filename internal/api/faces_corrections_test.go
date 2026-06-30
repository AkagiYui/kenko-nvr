package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

func corrEnv(t *testing.T) *Server {
	t.Helper()
	s := faceTestServer(t)
	if err := s.db.Cameras.Create(database.Camera{ID: "cam", Name: "cam", SourceType: database.SourceRTSP}); err != nil {
		t.Fatal(err)
	}
	start := time.UnixMilli(1_700_000_000_000)
	if err := s.db.Recordings.Create(database.Recording{
		ID: "rec", CameraID: "cam", Path: "rec.mp4",
		StartTime: database.MS(start), EndTime: database.MS(start.Add(time.Hour)), Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func addPT(t *testing.T, s *Server, personID, trackID string, nFaces int) {
	t.Helper()
	if err := s.db.Persons.Create(database.Person{ID: personID}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.FaceTracks.Create(database.FaceTrack{
		ID: trackID, RecordingID: "rec", CameraID: "cam", PersonID: personID,
		Embedding: make([]float32, 512), Dim: 512, FaceCount: nFaces, BestFaceID: trackID + "-f0",
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nFaces; i++ {
		if err := s.db.Faces.Create(database.Face{
			ID: fmt.Sprintf("%s-f%d", trackID, i), RecordingID: "rec", CameraID: "cam",
			PersonID: personID, TrackID: trackID, Embedding: make([]float32, 512), Dim: 512, Quality: 0.5,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Persons.Recount(personID); err != nil {
		t.Fatal(err)
	}
}

func TestMergePersons(t *testing.T) {
	s := corrEnv(t)
	addPT(t, s, "p1", "t1", 2)
	addPT(t, s, "p2", "t2", 1)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"sourceIds":["p2"],"targetId":"p1"}`)
	s.handleMergePersons(rec, httptest.NewRequest("POST", "/api/persons/merge", body))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if _, err := s.db.Persons.Get("p2"); err == nil {
		t.Error("source person p2 should be deleted")
	}
	p1, _ := s.db.Persons.Get("p1")
	if p1.FaceCount != 3 {
		t.Errorf("merged person should hold 3 faces, got %d", p1.FaceCount)
	}
	t2, _ := s.db.FaceTracks.Get("t2")
	if t2.PersonID != "p1" || !t2.Confirmed {
		t.Errorf("moved track should point to p1 and be confirmed: %+v", t2)
	}
}

func TestAssignTrackNewPerson(t *testing.T) {
	s := corrEnv(t)
	addPT(t, s, "p3", "t3", 1)

	rec := httptest.NewRecorder()
	s.handleAssignTrack(rec, reqWithParam("POST", "/api/tracks/t3/assign", "id", "t3", strings.NewReader(`{"newPerson":true}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var p database.Person
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.ID == "" || p.ID == "p3" {
		t.Fatalf("expected a fresh person, got %q", p.ID)
	}
	t3, _ := s.db.FaceTracks.Get("t3")
	if t3.PersonID != p.ID || !t3.Confirmed {
		t.Errorf("t3 should move to new person and be confirmed: %+v", t3)
	}
	if _, err := s.db.Persons.Get("p3"); err == nil {
		t.Error("emptied source person p3 should be deleted")
	}
}

func TestIgnoreFaceRecount(t *testing.T) {
	s := corrEnv(t)
	addPT(t, s, "p4", "t4", 2)

	rec := httptest.NewRecorder()
	s.handleIgnoreFace(rec, reqWithParam("POST", "/api/faces/t4-f0/ignore", "id", "t4-f0", strings.NewReader(`{"ignored":true}`)))
	if rec.Code != 204 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	p4, _ := s.db.Persons.Get("p4")
	if p4.FaceCount != 1 {
		t.Errorf("ignoring one face should leave face_count 1, got %d", p4.FaceCount)
	}
}

func TestCreateCannotLink(t *testing.T) {
	s := corrEnv(t)
	rec := httptest.NewRecorder()
	s.handleCreateLink(rec, httptest.NewRequest("POST", "/api/face/links", strings.NewReader(`{"kind":"cannot","aTrack":"t1","bTrack":"t2"}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	links, _ := s.db.PersonLinks.List()
	if len(links) != 1 || links[0].Kind != database.LinkCannot {
		t.Fatalf("link not stored: %+v", links)
	}
	// Invalid kind is rejected.
	rec = httptest.NewRecorder()
	s.handleCreateLink(rec, httptest.NewRequest("POST", "/api/face/links", strings.NewReader(`{"kind":"maybe","aTrack":"a","bTrack":"b"}`)))
	if rec.Code != 400 {
		t.Errorf("invalid kind should 400, got %d", rec.Code)
	}
}
