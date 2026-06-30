package rechls

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func ffmpegHas(t *testing.T, encoder string) bool {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return false
	}
	out, _ := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	return strings.Contains(string(out), encoder)
}

// TestEnsureTranscodesHEVCToH264HLS is the real scenario: a fragmented HEVC clip
// (which browsers struggle to play) is transcoded on demand to an H.264 HLS
// playlist + MPEG-TS segments that play everywhere and seek/resume cleanly.
func TestEnsureTranscodesHEVCToH264HLS(t *testing.T) {
	if !ffmpegHas(t, "libx265") {
		t.Skip("ffmpeg with libx265 required")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	// A fragmented HEVC mp4, like the recorder produces.
	gen := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=8:size=320x240:rate=10",
		"-c:v", "libx265", "-pix_fmt", "yuv420p",
		"-movflags", "frag_keyframe+empty_moov", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("gen hevc: %v: %s", err, out)
	}

	m := New(filepath.Join(dir, "cache"), nil /* software libx264 */, nil)
	defer m.Close()

	sid, err := m.Ensure(context.Background(), "rec1", src, 0)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// Playlist is a valid, segmented HLS manifest.
	pl, ok := m.Playlist(sid)
	if !ok {
		t.Fatal("no playlist path")
	}
	data, err := os.ReadFile(pl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "#EXTM3U") || !strings.Contains(string(data), "#EXTINF:") {
		t.Fatalf("not a segmented playlist:\n%s", data)
	}

	// The first segment exists and is H.264 (transcoded from HEVC).
	seg, ok := m.Segment(sid, "seg00000.ts")
	if !ok {
		t.Fatal("no segment path")
	}
	if _, err := os.Stat(seg); err != nil {
		t.Fatalf("first segment missing: %v", err)
	}
	codec, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", seg).Output()
	got := strings.TrimSpace(string(codec))
	if !strings.Contains(got, "h264") || strings.Contains(got, "hevc") {
		t.Errorf("segment should be transcoded to h264, got %q", got)
	}

	// Reusing the same (recording, offset) returns the same session, not a new one.
	sid2, err := m.Ensure(context.Background(), "rec1", src, 0)
	if err != nil || sid2 != sid {
		t.Fatalf("expected reused session: sid=%s sid2=%s err=%v", sid, sid2, err)
	}
}
