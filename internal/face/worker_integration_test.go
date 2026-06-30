package face

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// TestWorkerPipelineIntegration runs the full phase-1 pipeline against a live
// sidecar: sample frames from a recording, send them for inference, store faces,
// and complete the job. It needs FACE_SIDECAR_URL (e.g. http://127.0.0.1:8077)
// and ffmpeg. By default it uses a synthetic test-pattern clip (no faces) and
// only asserts the plumbing; set FACE_TEST_VIDEO to a clip containing faces to
// also assert that faces are detected and stored.
func TestWorkerPipelineIntegration(t *testing.T) {
	url := os.Getenv("FACE_SIDECAR_URL")
	if url == "" {
		t.Skip("set FACE_SIDECAR_URL to run the face pipeline integration test")
	}
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not on PATH")
	}
	ctx := context.Background()

	if h, err := NewClient(url).Health(ctx); err != nil || h.Status != "ok" {
		t.Fatalf("sidecar not healthy: %+v err=%v", h, err)
	}

	root := t.TempDir()
	rel := filepath.Join("cam", "clip.mp4")
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}

	expectFaces := false
	if v := os.Getenv("FACE_TEST_VIDEO"); v != "" {
		data, err := os.ReadFile(v)
		if err != nil {
			t.Fatalf("FACE_TEST_VIDEO: %v", err)
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			t.Fatal(err)
		}
		expectFaces = true
	} else {
		genTestVideo(t, abs, 3)
	}

	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Cameras.Create(database.Camera{ID: "cam", Name: "cam", SourceType: database.SourceRTSP}); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute)
	rec := database.Recording{
		ID: "rec", CameraID: "cam", Path: rel,
		StartTime: database.MS(start), EndTime: database.MS(start.Add(3 * time.Second)),
		DurationMS: 3000, Complete: true,
	}
	if err := db.Recordings.Create(rec); err != nil {
		t.Fatal(err)
	}
	if err := db.FaceJobs.Enqueue("rec"); err != nil {
		t.Fatal(err)
	}

	cfg := database.FaceConfig{
		Enabled: true, SidecarURL: url, SampleFPS: 4, MaxFramesPerJob: 100,
		BatchSize: 8, MinFaceSize: 0, DetThreshold: 0.5, MotionGated: false,
	}
	w := &Worker{DB: db, Root: root, FFmpegPath: "ffmpeg", ConfigFn: func() database.FaceConfig { return cfg }}

	did, err := w.processOne(ctx, cfg)
	if err != nil || !did {
		t.Fatalf("processOne: did=%v err=%v", did, err)
	}
	counts, _ := db.FaceJobs.Counts()
	if counts[database.FaceJobDone] != 1 {
		t.Fatalf("job not done: %v", counts)
	}
	n, _ := db.Faces.CountByRecording("rec")
	t.Logf("faces stored: %d", n)
	if expectFaces && n == 0 {
		t.Error("expected faces in FACE_TEST_VIDEO but none were stored")
	}
}
