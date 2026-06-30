package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/config"
	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/rechls"
)

// TestRecordingHLSEndpoints drives the HTTP path: start a transcode for a
// recording, then fetch the returned playlist and a segment through the handlers.
func TestRecordingHLSEndpoints(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg required")
	}
	root := t.TempDir()
	rel := "cam/clip.mp4"
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	gen := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=6:size=320x240:rate=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", abs)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("gen: %v: %s", err, out)
	}

	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.Cameras.Create(database.Camera{ID: "cam", Name: "cam", SourceType: database.SourceRTSP})
	start := time.Now()
	_ = db.Recordings.Create(database.Recording{
		ID: "rec1", CameraID: "cam", Path: rel,
		StartTime: database.MS(start), EndTime: database.MS(start.Add(6 * time.Second)),
		DurationMS: 6000, Complete: true,
	})

	s := &Server{
		cfg:    config.Config{Storage: config.StorageConfig{RecordingsDir: root}},
		db:     db,
		log:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		rechls: rechls.New(filepath.Join(t.TempDir(), "hls"), nil, nil),
	}
	defer s.rechls.Close()

	// Start the transcode session.
	startReq := httptest.NewRequest("GET", "/api/recordings/rec1/hls?from=0", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "rec1")
	startReq = startReq.WithContext(context.WithValue(startReq.Context(), chi.RouteCtxKey, rctx))
	startRec := httptest.NewRecorder()
	s.handleRecordingHLSStart(startRec, startReq)
	if startRec.Code != 200 {
		t.Fatalf("start status %d: %s", startRec.Code, startRec.Body)
	}
	var resp struct {
		Playlist string `json:"playlist"`
	}
	if err := json.Unmarshal(startRec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Playlist, "/api/rechls/rec1_0/") {
		t.Fatalf("unexpected playlist url: %s", resp.Playlist)
	}

	// Fetch the playlist through the file handler.
	serveFile := func(file string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/rechls/rec1_0/"+file, nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("sid", "rec1_0")
		rc.URLParams.Add("file", file)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
		rec := httptest.NewRecorder()
		s.handleHLSFile(rec, req)
		return rec
	}

	pl := serveFile("index.m3u8")
	if pl.Code != 200 || !strings.Contains(pl.Body.String(), "#EXTM3U") {
		t.Fatalf("playlist serve failed: %d %s", pl.Code, pl.Body)
	}
	if ct := pl.Header().Get("Content-Type"); !strings.Contains(ct, "mpegurl") {
		t.Errorf("playlist content-type: %s", ct)
	}

	seg := serveFile("seg00000.ts")
	if seg.Code != 200 || seg.Body.Len() == 0 {
		t.Fatalf("segment serve failed: %d len=%d", seg.Code, seg.Body.Len())
	}
	if ct := seg.Header().Get("Content-Type"); ct != "video/mp2t" {
		t.Errorf("segment content-type: %s", ct)
	}

	// Path traversal is rejected.
	bad := serveFile("../../etc/passwd")
	if bad.Code != 400 {
		t.Errorf("path traversal should be 400, got %d", bad.Code)
	}
}
