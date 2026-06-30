package face

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// TestRealFootageGroupingIntegration runs the entire phase-1+2 pipeline (sample
// -> infer -> store -> track -> assign) over real recordings on disk and reports
// how many faces, tracks and distinct people it found. It needs a live sidecar
// (FACE_SIDECAR_URL) and FACE_REAL_DIR pointing at a directory of .mp4 files
// (e.g. data/recordings/<cam>/<date>). It is skipped by default.
func TestRealFootageGroupingIntegration(t *testing.T) {
	url := os.Getenv("FACE_SIDECAR_URL")
	dir := os.Getenv("FACE_REAL_DIR")
	if url == "" || dir == "" {
		t.Skip("set FACE_SIDECAR_URL and FACE_REAL_DIR to run the real-footage test")
	}
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not on PATH")
	}
	ctx := context.Background()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".mp4" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	maxFiles := 4
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	if len(files) == 0 {
		t.Skip("no .mp4 files in FACE_REAL_DIR")
	}

	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Cameras.Create(database.Camera{ID: "cam", Name: "cam", SourceType: database.SourceRTSP}); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-24 * time.Hour)
	for i, name := range files {
		recID := name
		start := base.Add(time.Duration(i) * time.Hour)
		if err := db.Recordings.Create(database.Recording{
			ID: recID, CameraID: "cam", Path: name,
			StartTime: database.MS(start), EndTime: database.MS(start.Add(10 * time.Minute)),
			DurationMS: 600_000, Complete: true,
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.FaceJobs.Enqueue(recID); err != nil {
			t.Fatal(err)
		}
	}

	cfg := database.DefaultFaceConfig()
	cfg.Enabled = true
	cfg.SidecarURL = url
	// Sample sparsely across the whole file (a person may appear at any point in a
	// 10-minute segment), bounded by the frame cap. Relax the size/score gates so
	// distant faces in a wide camera shot are also counted.
	cfg.SampleFPS = 0.2
	cfg.MaxFramesPerJob = 150
	cfg.MotionGated = false
	cfg.BatchSize = 8
	cfg.MinFaceSize = 0
	cfg.DetThreshold = 0.4

	w := &Worker{
		DB: db, Root: dir, FFmpegPath: "ffmpeg",
		ConfigFn: func() database.FaceConfig { return cfg },
		Assigner: &Gallery{DB: db},
	}
	for {
		did, err := w.processOne(ctx, cfg)
		if err != nil {
			t.Fatalf("processOne: %v", err)
		}
		if !did {
			break
		}
	}

	faces := 0
	for _, name := range files {
		n, _ := db.Faces.CountByRecording(name)
		faces += n
	}
	tracks, _ := db.FaceTracks.List(database.FaceTrackFilter{Limit: 10000})
	persons, _ := db.Persons.List(database.PersonFilter{Limit: 10000})
	named := 0
	for _, p := range persons {
		if p.FaceCount > 0 {
			named++
		}
	}
	t.Logf("real footage: files=%d faces=%d tracks=%d persons=%d (non-empty=%d)",
		len(files), faces, len(tracks), len(persons), named)
	for _, p := range persons {
		t.Logf("  person %s: faces=%d firstSeen=%s lastSeen=%s",
			p.ID[:8], p.FaceCount, p.FirstSeen.Time.Format("15:04:05"), p.LastSeen.Time.Format("15:04:05"))
	}
	if faces == 0 {
		t.Skip("no faces found in this footage window")
	}
	if len(persons) == 0 || len(tracks) == 0 {
		t.Fatalf("faces found (%d) but no tracks/persons formed", faces)
	}
}
