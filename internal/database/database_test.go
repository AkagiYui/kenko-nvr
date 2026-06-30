package database

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCameraCRUD(t *testing.T) {
	db := openTest(t)

	cam := Camera{ID: "cam1", Name: "Front", SourceType: SourceRTSP, URL: "rtsp://x", Record: true, Enabled: true}
	if err := db.Cameras.Create(cam); err != nil {
		t.Fatal(err)
	}

	got, err := db.Cameras.Get("cam1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Front" || got.SourceType != SourceRTSP || !got.Record {
		t.Errorf("unexpected camera: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("createdAt not set")
	}

	got.Name = "Front Door"
	got.Record = false
	if err := db.Cameras.Update(got); err != nil {
		t.Fatal(err)
	}
	got2, _ := db.Cameras.Get("cam1")
	if got2.Name != "Front Door" || got2.Record {
		t.Errorf("update not applied: %+v", got2)
	}

	list, err := db.Cameras.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d, err=%v", len(list), err)
	}

	if err := db.Cameras.Delete("cam1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Cameras.Get("cam1"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestCameraNotFound(t *testing.T) {
	db := openTest(t)
	if _, err := db.Cameras.Get("nope"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if err := db.Cameras.Update(Camera{ID: "nope", Name: "x"}); err != ErrNotFound {
		t.Errorf("update nonexistent: expected ErrNotFound, got %v", err)
	}
	if err := db.Cameras.Delete("nope"); err != ErrNotFound {
		t.Errorf("delete nonexistent: expected ErrNotFound, got %v", err)
	}
}

func TestRecordingLifecycle(t *testing.T) {
	db := openTest(t)
	if err := db.Cameras.Create(Camera{ID: "c", Name: "c", SourceType: SourceRTSP}); err != nil {
		t.Fatal(err)
	}

	start := time.Now().Add(-time.Hour)
	rec := Recording{ID: "r1", CameraID: "c", Path: "c/r1.mp4", StartTime: MS(start)}
	if err := db.Recordings.Create(rec); err != nil {
		t.Fatal(err)
	}

	// not complete yet -> not in pending uploads or oldest-complete
	if recs, _ := db.Recordings.PendingUploads(10); len(recs) != 0 {
		t.Errorf("expected no pending uploads before finalize, got %d", len(recs))
	}

	end := start.Add(10 * time.Minute)
	if err := db.Recordings.Finalize("r1", end, 600000, 1234); err != nil {
		t.Fatal(err)
	}

	got, _ := db.Recordings.Get("r1")
	if !got.Complete || got.SizeBytes != 1234 || got.DurationMS != 600000 {
		t.Errorf("finalize not applied: %+v", got)
	}

	total, _ := db.Recordings.TotalSize()
	if total != 1234 {
		t.Errorf("total size = %d", total)
	}

	pend, _ := db.Recordings.PendingUploads(10)
	if len(pend) != 1 {
		t.Fatalf("expected 1 pending upload, got %d", len(pend))
	}
	if err := db.Recordings.MarkUploaded("r1", "bucket/key.mp4"); err != nil {
		t.Fatal(err)
	}
	if pend, _ := db.Recordings.PendingUploads(10); len(pend) != 0 {
		t.Errorf("expected 0 pending after upload, got %d", len(pend))
	}

	oldUploaded, _ := db.Recordings.OldestComplete(10, true)
	if len(oldUploaded) != 1 {
		t.Errorf("expected uploaded recording in OldestComplete(onlyUploaded), got %d", len(oldUploaded))
	}

	// After the local file is freed (kept only on S3), the row stays but is
	// excluded from disk accounting and retention candidates, while remaining
	// fetchable by Get/List for S3 playback.
	if err := db.Recordings.MarkLocalRemoved("r1"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.Recordings.Get("r1")
	if !got.LocalRemoved || got.S3Key != "bucket/key.mp4" {
		t.Errorf("expected localRemoved with S3 key preserved, got %+v", got)
	}
	if total, _ := db.Recordings.TotalSize(); total != 0 {
		t.Errorf("local_removed recording should not count toward TotalSize, got %d", total)
	}
	if old, _ := db.Recordings.OldestComplete(10, true); len(old) != 0 {
		t.Errorf("local_removed recording should not be a retention candidate, got %d", len(old))
	}
}

func TestRecordingListOverlapping(t *testing.T) {
	db := openTest(t)
	if err := db.Cameras.Create(Camera{ID: "c", Name: "c", SourceType: SourceRTSP}); err != nil {
		t.Fatal(err)
	}
	if err := db.Cameras.Create(Camera{ID: "other", Name: "o", SourceType: SourceRTSP}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	mk := func(id string, cam string, startMin, durMin int, complete bool) {
		start := base.Add(time.Duration(startMin) * time.Minute)
		if err := db.Recordings.Create(Recording{ID: id, CameraID: cam, Path: id + ".mp4", StartTime: MS(start)}); err != nil {
			t.Fatal(err)
		}
		if complete {
			end := start.Add(time.Duration(durMin) * time.Minute)
			if err := db.Recordings.Finalize(id, end, int64(durMin)*60000, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Three 10-min segments back-to-back, plus an in-progress one and a clip on
	// a different camera.
	mk("a", "c", 0, 10, true)     // 12:00–12:10
	mk("b", "c", 10, 10, true)    // 12:10–12:20
	mk("c", "c", 20, 10, true)    // 12:20–12:30
	mk("live", "c", 30, 0, false) // 12:30–(now); in progress
	mk("x", "other", 10, 10, true)

	// An event at 12:10:30 falls inside segment b only.
	got, err := db.Recordings.ListOverlapping([]string{"c"}, base.Add(10*time.Minute+30*time.Second), base.Add(10*time.Minute+45*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("event inside b: expected [b], got %v", ids(got))
	}

	// An event straddling the b/c boundary overlaps both.
	got, _ = db.Recordings.ListOverlapping([]string{"c"}, base.Add(19*time.Minute), base.Add(21*time.Minute))
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "c" {
		t.Fatalf("event across b/c boundary: expected [b c], got %v", ids(got))
	}

	// An event in the in-progress segment matches it despite the unset end time.
	got, _ = db.Recordings.ListOverlapping([]string{"c"}, base.Add(40*time.Minute), base.Add(41*time.Minute))
	if len(got) != 1 || got[0].ID != "live" {
		t.Fatalf("event in live segment: expected [live], got %v", ids(got))
	}

	// The camera filter excludes the other camera's overlapping clip.
	got, _ = db.Recordings.ListOverlapping([]string{"c"}, base.Add(12*time.Minute), base.Add(13*time.Minute))
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("camera filter: expected [b], got %v", ids(got))
	}
}

func ids(recs []Recording) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func TestRecordingFilterAndCascade(t *testing.T) {
	db := openTest(t)
	db.Cameras.Create(Camera{ID: "a", Name: "a"})
	db.Cameras.Create(Camera{ID: "b", Name: "b"})

	base := time.Now()
	for i, camID := range []string{"a", "a", "b"} {
		db.Recordings.Create(Recording{
			ID: string(rune('x' + i)), CameraID: camID,
			Path: camID + ".mp4", StartTime: MS(base.Add(time.Duration(i) * time.Minute)),
		})
	}

	aRecs, _ := db.Recordings.List(RecordingFilter{CameraID: "a"})
	if len(aRecs) != 2 {
		t.Errorf("camera a should have 2 recordings, got %d", len(aRecs))
	}

	// FK cascade: deleting camera a removes its recordings.
	if err := db.Cameras.Delete("a"); err != nil {
		t.Fatal(err)
	}
	all, _ := db.Recordings.List(RecordingFilter{})
	if len(all) != 1 {
		t.Errorf("after cascade delete expected 1 recording, got %d", len(all))
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	db := openTest(t)

	// defaults when unset
	if p, _ := db.Settings.Retention(); p.MaxAgeDays != DefaultRetentionPolicy().MaxAgeDays {
		t.Error("expected default retention when unset")
	}

	want := RetentionPolicy{Enabled: true, MaxAgeDays: 14, MaxTotalSizeGB: 50, MinFreeSpaceGB: 2}
	if err := db.Settings.SetRetention(want); err != nil {
		t.Fatal(err)
	}
	got, _ := db.Settings.Retention()
	if got != want {
		t.Errorf("retention round-trip: got %+v want %+v", got, want)
	}

	s3 := S3Config{Enabled: true, Endpoint: "s3.local", Bucket: "b", ProxyURL: "http://p:3128"}
	db.Settings.SetS3(s3)
	if g, _ := db.Settings.S3(); g.ProxyURL != "http://p:3128" || g.Bucket != "b" {
		t.Errorf("s3 round-trip failed: %+v", g)
	}
}
