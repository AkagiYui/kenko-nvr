package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

func faceTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{db: db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func reqWithParam(method, target, key, val string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func seedPerson(t *testing.T, s *Server) {
	t.Helper()
	db := s.db
	if err := db.Cameras.Create(database.Camera{ID: "cam", Name: "cam", SourceType: database.SourceRTSP}); err != nil {
		t.Fatal(err)
	}
	start := time.UnixMilli(1_700_000_000_000)
	if err := db.Recordings.Create(database.Recording{
		ID: "rec1", CameraID: "cam", Path: "cam/r1.mp4",
		StartTime: database.MS(start), EndTime: database.MS(start.Add(time.Minute)), Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Persons.Create(database.Person{ID: "p1", Name: "Alice", Named: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.FaceTracks.Create(database.FaceTrack{
		ID: "tr1", RecordingID: "rec1", CameraID: "cam", PersonID: "p1",
		StartTS: database.MS(start), EndTS: database.MS(start.Add(5 * time.Second)),
		FaceCount: 2, BestFaceID: "f1", BestOffsetMS: 1500, Embedding: make([]float32, 512), Dim: 512,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"f1", "f2"} {
		if err := db.Faces.Create(database.Face{
			ID: id, RecordingID: "rec1", CameraID: "cam", PersonID: "p1", TrackID: "tr1",
			Timestamp: database.MS(start), Embedding: make([]float32, 512), Dim: 512,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Persons.Recount("p1"); err != nil {
		t.Fatal(err)
	}
}

func TestHandleListPersons(t *testing.T) {
	s := faceTestServer(t)
	seedPerson(t, s)

	rec := httptest.NewRecorder()
	s.handleListPersons(rec, httptest.NewRequest("GET", "/api/persons", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var persons []database.Person
	if err := json.Unmarshal(rec.Body.Bytes(), &persons); err != nil {
		t.Fatal(err)
	}
	if len(persons) != 1 || persons[0].Name != "Alice" || persons[0].FaceCount != 2 {
		t.Fatalf("unexpected persons: %+v", persons)
	}
}

func TestHandleGetPersonWithRecordings(t *testing.T) {
	s := faceTestServer(t)
	seedPerson(t, s)

	rec := httptest.NewRecorder()
	s.handleGetPerson(rec, reqWithParam("GET", "/api/persons/p1?withRecordings=1", "id", "p1", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var pd personDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil {
		t.Fatal(err)
	}
	if pd.ID != "p1" || len(pd.Appearances) != 1 {
		t.Fatalf("unexpected detail: %+v", pd)
	}
	if pd.Appearances[0].BestOffsetMS != 1500 || pd.Appearances[0].RecordingID != "rec1" {
		t.Errorf("appearance playback target wrong: %+v", pd.Appearances[0])
	}
	if len(pd.Recordings) != 1 || pd.Recordings[0].ID != "rec1" {
		t.Errorf("recordings not attached: %+v", pd.Recordings)
	}
	// Embeddings must never be serialised (biometric data).
	if strings.Contains(rec.Body.String(), "embedding") {
		t.Error("response leaked embedding data")
	}
}

func TestHandleListFacesNoEmbedding(t *testing.T) {
	s := faceTestServer(t)
	seedPerson(t, s)

	rec := httptest.NewRecorder()
	s.handleListFaces(rec, httptest.NewRequest("GET", "/api/faces?personId=p1", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var faces []database.Face
	if err := json.Unmarshal(rec.Body.Bytes(), &faces); err != nil {
		t.Fatal(err)
	}
	if len(faces) != 2 {
		t.Fatalf("want 2 faces, got %d", len(faces))
	}
	if strings.Contains(rec.Body.String(), "embedding") {
		t.Error("faces response leaked embedding data")
	}
}

func TestHandleFaceSettingsSanitise(t *testing.T) {
	s := faceTestServer(t)
	// Submit out-of-range values; expect them clamped to defaults on read-back.
	body := strings.NewReader(`{"enabled":true,"sidecarURL":"http://x:8077","sampleFps":0,"detThreshold":5,"matchThreshold":0.6,"reviewThreshold":0.9}`)
	rec := httptest.NewRecorder()
	s.handleSetFaceSettings(rec, httptest.NewRequest("PUT", "/api/settings/face", body))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got, err := s.db.Settings.Face()
	if err != nil {
		t.Fatal(err)
	}
	d := database.DefaultFaceConfig()
	if !got.Enabled || got.SampleFPS != d.SampleFPS || got.DetThreshold != d.DetThreshold {
		t.Errorf("sanitise failed: %+v", got)
	}
	// reviewThreshold (0.9) exceeded matchThreshold (0.6) -> reset to default.
	if got.ReviewThreshold != d.ReviewThreshold {
		t.Errorf("reviewThreshold not reset: %v", got.ReviewThreshold)
	}
}
