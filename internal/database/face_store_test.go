package database

import (
	"math"
	"testing"
	"time"
)

// seedRecording inserts a camera + a completed recording (faces/face_jobs have
// FKs to recordings -> cameras) and returns the recording ID.
func seedRecording(t *testing.T, db *DB) string {
	t.Helper()
	if err := db.Cameras.Create(Camera{ID: "cam-face", Name: "FaceCam", SourceType: SourceRTSP, Enabled: true}); err != nil {
		t.Fatalf("camera: %v", err)
	}
	start := time.UnixMilli(1_700_000_000_000)
	rec := Recording{
		ID: "rec-face", CameraID: "cam-face", Path: "cam/clip.mp4",
		StartTime: MS(start), EndTime: MS(start.Add(10 * time.Minute)),
		DurationMS: 600_000, Complete: true,
	}
	if err := db.Recordings.Create(rec); err != nil {
		t.Fatalf("recording: %v", err)
	}
	return rec.ID
}

func TestFaceRoundTrip(t *testing.T) {
	db := openTest(t)
	recID := seedRecording(t, db)

	emb := make([]float32, 512)
	for i := range emb {
		emb[i] = float32(i) * 0.001
	}
	start := time.UnixMilli(1_700_000_005_000)
	if err := db.Faces.Create(Face{
		RecordingID: recID, CameraID: "cam-face",
		Timestamp: MS(start), OffsetMS: 5000,
		BBox: BBox{X: 10, Y: 20, W: 30, H: 40}, DetScore: 0.97, Quality: 0.81,
		Embedding: emb, Dim: 512, Model: "buffalo_l",
	}); err != nil {
		t.Fatalf("create face: %v", err)
	}

	list, err := db.Faces.List(FaceFilter{RecordingID: recID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 face, got %d", len(list))
	}
	got := list[0]
	if got.ID == "" {
		t.Error("face ID not auto-assigned")
	}
	if len(got.Embedding) != 512 {
		t.Fatalf("embedding dim: want 512, got %d", len(got.Embedding))
	}
	if math.Abs(float64(got.Embedding[100])-0.1) > 1e-5 {
		t.Errorf("embedding[100]: want ~0.1, got %v", got.Embedding[100])
	}
	if got.BBox.W != 30 || got.DetScore != 0.97 || got.OffsetMS != 5000 {
		t.Errorf("field round-trip mismatch: %+v", got)
	}

	n, err := db.Faces.CountByRecording(recID)
	if err != nil || n != 1 {
		t.Fatalf("count: %d err %v", n, err)
	}
}

func TestPersonRecountAndMeta(t *testing.T) {
	db := openTest(t)
	recID := seedRecording(t, db)
	if err := db.Persons.Create(Person{ID: "p1"}); err != nil {
		t.Fatalf("person: %v", err)
	}
	t1 := time.UnixMilli(1_700_000_010_000)
	t2 := time.UnixMilli(1_700_000_020_000)
	for _, ts := range []time.Time{t1, t2} {
		if err := db.Faces.Create(Face{RecordingID: recID, CameraID: "cam-face", PersonID: "p1", Timestamp: MS(ts), Dim: 512, Embedding: make([]float32, 512)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := db.Persons.Recount("p1")
	if err != nil || n != 2 {
		t.Fatalf("recount: %d err %v", n, err)
	}
	p, _ := db.Persons.Get("p1")
	if p.FaceCount != 2 || !p.FirstSeen.Time.Equal(t1) || !p.LastSeen.Time.Equal(t2) {
		t.Errorf("person window wrong: %+v", p)
	}
	if err := db.Persons.UpdateMeta("p1", "Alice", "vip", ""); err != nil {
		t.Fatal(err)
	}
	p, _ = db.Persons.Get("p1")
	if p.Name != "Alice" || !p.Named {
		t.Errorf("meta not applied: %+v", p)
	}
}

func TestFaceJobLifecycle(t *testing.T) {
	db := openTest(t)
	recID := seedRecording(t, db)

	if err := db.FaceJobs.Enqueue(recID); err != nil {
		t.Fatal(err)
	}
	// Duplicate enqueue is ignored (unique on recording_id).
	if err := db.FaceJobs.Enqueue(recID); err != nil {
		t.Fatal(err)
	}
	if c, _ := db.FaceJobs.Counts(); c[FaceJobPending] != 1 {
		t.Fatalf("want 1 pending, got %v", c)
	}

	job, ok, err := db.FaceJobs.ClaimNext(maxAttemptsTest)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if job.State != FaceJobRunning || job.Attempts != 1 {
		t.Errorf("claimed job state/attempts wrong: %+v", job)
	}
	// Nothing else claimable while it runs.
	if _, ok2, _ := db.FaceJobs.ClaimNext(maxAttemptsTest); ok2 {
		t.Error("a second claim should find nothing")
	}

	// Fail -> retryable (attempts 1 < max), reclaimed with attempts 2.
	if err := db.FaceJobs.Fail(job.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	job2, ok, _ := db.FaceJobs.ClaimNext(maxAttemptsTest)
	if !ok || job2.Attempts != 2 {
		t.Fatalf("retry claim: ok=%v attempts=%d", ok, job2.Attempts)
	}

	// RequeueRunning (crash recovery) puts it back to pending.
	if n, _ := db.FaceJobs.RequeueRunning(); n != 1 {
		t.Errorf("requeue: want 1, got %d", n)
	}
	if c, _ := db.FaceJobs.Counts(); c[FaceJobPending] != 1 {
		t.Errorf("after requeue want 1 pending, got %v", c)
	}

	// Complete is terminal.
	job3, _, _ := db.FaceJobs.ClaimNext(maxAttemptsTest)
	if err := db.FaceJobs.Complete(job3.ID, 5); err != nil {
		t.Fatal(err)
	}
	if c, _ := db.FaceJobs.Counts(); c[FaceJobDone] != 1 {
		t.Errorf("want 1 done, got %v", c)
	}
	if _, ok, _ := db.FaceJobs.ClaimNext(maxAttemptsTest); ok {
		t.Error("completed job should not be claimable")
	}
}

const maxAttemptsTest = 3
