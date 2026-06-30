package face

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// mkPersonTrack creates a person (if new), a track with the given representative
// embedding, and nFaces faces under it, then recounts. It models the state the
// incremental assigner leaves behind so re-clustering can be exercised directly.
func mkPersonTrack(t *testing.T, db *database.DB, recID, personID, trackID string, rep []float32, confirmed bool, nFaces int) {
	t.Helper()
	if _, err := db.Persons.Get(personID); err != nil {
		if err := db.Persons.Create(database.Person{ID: personID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.FaceTracks.Create(database.FaceTrack{
		ID: trackID, RecordingID: recID, CameraID: "cam", PersonID: personID,
		Embedding: rep, Dim: 512, FaceCount: nFaces, Confirmed: confirmed,
		BestFaceID: trackID + "-f0", StartTS: database.MS(time.UnixMilli(1_700_000_000_000)),
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nFaces; i++ {
		if err := db.Faces.Create(database.Face{
			ID: fmt.Sprintf("%s-f%d", trackID, i), RecordingID: recID, CameraID: "cam",
			PersonID: personID, TrackID: trackID, Embedding: rep, Dim: 512, Quality: 0.8,
			Timestamp: database.MS(time.UnixMilli(1_700_000_000_000)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Persons.Recount(personID); err != nil {
		t.Fatal(err)
	}
}

func clusterTestDB(t *testing.T) *database.DB {
	db := openFaceTestDB(t)
	start := time.UnixMilli(1_700_000_000_000)
	if err := db.Recordings.Create(database.Recording{
		ID: "rec", CameraID: "cam", Path: "rec.mp4",
		StartTime: database.MS(start), EndTime: database.MS(start.Add(time.Hour)), Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestReclusterMergesOverSplit(t *testing.T) {
	db := clusterTestDB(t)
	// Two anonymous persons that are really the same face (over-split).
	mkPersonTrack(t, db, "rec", "p1", "t1", noisy(vecA(), 1), false, 3)
	mkPersonTrack(t, db, "rec", "p2", "t2", noisy(vecA(), 2), false, 2)

	c := &Clusterer{DB: db}
	cfg := database.DefaultFaceConfig()
	res, err := c.Recluster(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.PersonsAfter != 1 {
		t.Fatalf("want 1 person after merge, got %d (moved=%d deleted=%d)", res.PersonsAfter, res.Moved, res.PersonsDeleted)
	}
	persons, _ := db.Persons.List(database.PersonFilter{Limit: 10})
	if len(persons) != 1 || persons[0].FaceCount != 5 {
		t.Fatalf("merged person should hold all 5 faces: %+v", persons)
	}
}

func TestReclusterKeepsDifferentPeople(t *testing.T) {
	db := clusterTestDB(t)
	mkPersonTrack(t, db, "rec", "p1", "t1", noisy(vecA(), 1), false, 3)
	mkPersonTrack(t, db, "rec", "p2", "t2", noisy(vecB(), 2), false, 3)

	c := &Clusterer{DB: db}
	res, err := c.Recluster(context.Background(), database.DefaultFaceConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.PersonsAfter != 2 || res.Moved != 0 {
		t.Fatalf("distinct people must stay split: after=%d moved=%d", res.PersonsAfter, res.Moved)
	}
}

func TestReclusterRespectsConfirmedAnchor(t *testing.T) {
	db := clusterTestDB(t)
	// p1 is confirmed (operator-locked); p2 is an anonymous duplicate of p1.
	mkPersonTrack(t, db, "rec", "p1", "t1", noisy(vecA(), 1), true, 4)
	mkPersonTrack(t, db, "rec", "p2", "t2", noisy(vecA(), 2), false, 2)

	c := &Clusterer{DB: db}
	res, err := c.Recluster(context.Background(), database.DefaultFaceConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.PersonsAfter != 1 {
		t.Fatalf("anonymous dup should fold into confirmed anchor: after=%d", res.PersonsAfter)
	}
	// The surviving person must be the confirmed one (p1), and t2 must now point to it.
	t2, _ := db.FaceTracks.Get("t2")
	if t2.PersonID != "p1" {
		t.Fatalf("t2 should join confirmed person p1, got %s", t2.PersonID)
	}
}

func TestReclusterHonoursCannotLink(t *testing.T) {
	db := clusterTestDB(t)
	mkPersonTrack(t, db, "rec", "p1", "t1", noisy(vecA(), 1), false, 3)
	mkPersonTrack(t, db, "rec", "p2", "t2", noisy(vecA(), 2), false, 3)
	if _, err := db.PersonLinks.Create(database.PersonLink{Kind: database.LinkCannot, ATrack: "t1", BTrack: "t2"}); err != nil {
		t.Fatal(err)
	}

	c := &Clusterer{DB: db}
	res, err := c.Recluster(context.Background(), database.DefaultFaceConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.PersonsAfter != 2 {
		t.Fatalf("cannot-linked tracks must stay separate despite similarity: after=%d", res.PersonsAfter)
	}
}
