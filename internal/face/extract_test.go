package face

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

func ffmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// genTestVideo writes a synthetic test-pattern mp4 (no faces) for plumbing tests.
func genTestVideo(t *testing.T, path string, seconds int) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=duration=%d:size=320x240:rate=10", seconds),
		"-pix_fmt", "yuv420p", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gen test video: %v: %s", err, out)
	}
}

func TestExtractRange(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not on PATH")
	}
	vid := filepath.Join(t.TempDir(), "t.mp4")
	genTestVideo(t, vid, 3)

	frames, err := ExtractRange(context.Background(), "ffmpeg", vid, 0, 0, 2, 100, 0)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	// 3s at 2fps ≈ 6 frames (allow a small boundary tolerance).
	if len(frames) < 5 || len(frames) > 7 {
		t.Fatalf("want ~6 frames, got %d", len(frames))
	}
	if frames[0].OffsetMS != 0 || frames[1].OffsetMS != 500 || frames[2].OffsetMS != 1000 {
		t.Errorf("offsets wrong: %d %d %d", frames[0].OffsetMS, frames[1].OffsetMS, frames[2].OffsetMS)
	}
	// Each frame is a JPEG (SOI marker FF D8).
	for i, f := range frames {
		if len(f.JPEG) < 2 || f.JPEG[0] != 0xFF || f.JPEG[1] != 0xD8 {
			t.Fatalf("frame %d is not a JPEG", i)
		}
	}
}

func TestExtractRangeWindow(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not on PATH")
	}
	vid := filepath.Join(t.TempDir(), "t.mp4")
	genTestVideo(t, vid, 4)

	// A 1-second window starting at t=1s, sampled at 2fps -> ~2 frames, offsets
	// measured from the recording start (1000ms, 1500ms).
	frames, err := ExtractRange(context.Background(), "ffmpeg", vid, 1, 1, 2, 100, 160)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(frames) < 1 || len(frames) > 3 {
		t.Fatalf("want ~2 frames, got %d", len(frames))
	}
	if frames[0].OffsetMS != 1000 {
		t.Errorf("first window offset: want 1000, got %d", frames[0].OffsetMS)
	}
}

func TestExtractFrameCap(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not on PATH")
	}
	vid := filepath.Join(t.TempDir(), "t.mp4")
	genTestVideo(t, vid, 5)
	frames, err := ExtractRange(context.Background(), "ffmpeg", vid, 0, 0, 4, 3, 0)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("frame cap not honoured: got %d, want 3", len(frames))
	}
}
