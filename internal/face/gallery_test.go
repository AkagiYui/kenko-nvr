package face

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// vecA and vecB are two orthogonal unit embeddings (cos = 0), standing in for
// two clearly different people. noisy() perturbs one slightly to mimic the same
// person seen in another frame (cos stays ~1).
func vecA() []float32 {
	v := make([]float32, 512)
	for i := 0; i < 256; i++ {
		v[i] = 1
	}
	return normalize(v)
}

func vecB() []float32 {
	v := make([]float32, 512)
	for i := 256; i < 512; i++ {
		v[i] = 1
	}
	return normalize(v)
}

func noisy(base []float32, seed int) []float32 {
	v := make([]float32, len(base))
	copy(v, base)
	for i := range v {
		v[i] += float32((i+seed)%7) * 0.0005
	}
	return normalize(v)
}

func synthFace(tag string, emb []float32, offsetMS int64, x float64) database.Face {
	return database.Face{
		Model: tag, Embedding: emb, Dim: 512, OffsetMS: offsetMS,
		Timestamp: database.MS(time.UnixMilli(1_700_000_000_000 + offsetMS)),
		BBox:      database.BBox{X: x, Y: 100, W: 80, H: 80},
		DetScore:  0.9, Quality: 0.8,
	}
}

func TestBuildTracksTwoPeople(t *testing.T) {
	var faces []database.Face
	for _, off := range []int64{0, 500, 1000, 1500} {
		faces = append(faces, synthFace("A", noisy(vecA(), int(off)), off, 100))
		faces = append(faces, synthFace("B", noisy(vecB(), int(off)), off, 400))
	}
	tracks := BuildTracks(faces, DefaultTrackParams())
	if len(tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(tracks))
	}
	for _, tr := range tracks {
		tag := tr.Faces[0].Model
		if len(tr.Faces) != 4 {
			t.Errorf("track %s: want 4 faces, got %d", tag, len(tr.Faces))
		}
		for _, f := range tr.Faces {
			if f.Model != tag {
				t.Errorf("track mixes people: %s and %s", tag, f.Model)
			}
		}
	}
}

func openFaceTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Cameras.Create(database.Camera{ID: "cam", Name: "cam", SourceType: database.SourceRTSP}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedFaceRecording(t *testing.T, db *database.DB, id string, faces []database.Face) {
	t.Helper()
	start := time.UnixMilli(1_700_000_000_000)
	if err := db.Recordings.Create(database.Recording{
		ID: id, CameraID: "cam", Path: id + ".mp4",
		StartTime: database.MS(start), EndTime: database.MS(start.Add(time.Minute)),
		DurationMS: 60000, Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, f := range faces {
		f.RecordingID = id
		f.CameraID = "cam"
		if err := db.Faces.Create(f); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGalleryAssignAndCrossRecordingMatch(t *testing.T) {
	db := openFaceTestDB(t)
	g := &Gallery{DB: db}
	cfg := database.DefaultFaceConfig()
	cfg.MatchThreshold = 0.45

	// Recording 1: persons A and B.
	var r1 []database.Face
	for _, off := range []int64{0, 500, 1000} {
		r1 = append(r1, synthFace("A", noisy(vecA(), int(off)), off, 100))
		r1 = append(r1, synthFace("B", noisy(vecB(), int(off)), off, 400))
	}
	seedFaceRecording(t, db, "rec1", r1)
	if err := g.AssignRecording(context.Background(), "rec1", cfg); err != nil {
		t.Fatalf("assign rec1: %v", err)
	}

	persons, _ := db.Persons.List(database.PersonFilter{Limit: 100})
	if len(persons) != 2 {
		t.Fatalf("want 2 persons after rec1, got %d", len(persons))
	}
	tracks1, _ := db.FaceTracks.List(database.FaceTrackFilter{RecordingID: "rec1"})
	if len(tracks1) != 2 {
		t.Fatalf("want 2 tracks for rec1, got %d", len(tracks1))
	}
	// Identify which person is A (its rep is closest to vecA).
	personA := ""
	for _, tr := range tracks1 {
		if dot(normalize(tr.Embedding), vecA()) > 0.9 {
			personA = tr.PersonID
		}
	}
	if personA == "" {
		t.Fatal("could not identify person A among rec1 tracks")
	}

	// Recording 2: person A again -> must match the existing person, not create a
	// new one.
	var r2 []database.Face
	for _, off := range []int64{0, 500, 1000} {
		r2 = append(r2, synthFace("A", noisy(vecA(), int(off)+3), off, 120))
	}
	seedFaceRecording(t, db, "rec2", r2)
	if err := g.AssignRecording(context.Background(), "rec2", cfg); err != nil {
		t.Fatalf("assign rec2: %v", err)
	}

	persons, _ = db.Persons.List(database.PersonFilter{Limit: 100})
	if len(persons) != 2 {
		t.Fatalf("cross-recording match failed: want 2 persons, got %d", len(persons))
	}
	tracks2, _ := db.FaceTracks.List(database.FaceTrackFilter{RecordingID: "rec2"})
	if len(tracks2) != 1 || tracks2[0].PersonID != personA {
		t.Fatalf("rec2 track should match person A (%s), got %+v", personA, tracks2)
	}
	// Person A's denormalised count should now span both recordings (3 + 3 faces).
	pa, _ := db.Persons.Get(personA)
	if pa.FaceCount != 6 {
		t.Errorf("person A face_count: want 6, got %d", pa.FaceCount)
	}
}

func TestGalleryIdempotent(t *testing.T) {
	db := openFaceTestDB(t)
	g := &Gallery{DB: db}
	cfg := database.DefaultFaceConfig()
	var faces []database.Face
	for _, off := range []int64{0, 500} {
		faces = append(faces, synthFace("A", noisy(vecA(), int(off)), off, 100))
	}
	seedFaceRecording(t, db, "rec1", faces)
	if err := g.AssignRecording(context.Background(), "rec1", cfg); err != nil {
		t.Fatal(err)
	}
	// Re-running must not duplicate tracks or persons.
	if err := g.AssignRecording(context.Background(), "rec1", cfg); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.FaceTracks.CountByRecording("rec1"); n != 1 {
		t.Errorf("want 1 track after re-run, got %d", n)
	}
	persons, _ := db.Persons.List(database.PersonFilter{Limit: 100})
	if len(persons) != 1 {
		t.Errorf("want 1 person after re-run, got %d", len(persons))
	}
}
