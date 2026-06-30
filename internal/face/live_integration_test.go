package face

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
)

// TestLiveFrameDetectionIntegration exercises the realtime path's core: ffmpeg
// emits an MJPEG stream (as it would from a live feed), splitMJPEG carves frames,
// and frameHasFace asks the sidecar. It needs FACE_SIDECAR_URL, FACE_TEST_VIDEO
// (a clip containing faces) and ffmpeg. Skipped by default.
func TestLiveFrameDetectionIntegration(t *testing.T) {
	url := os.Getenv("FACE_SIDECAR_URL")
	vid := os.Getenv("FACE_TEST_VIDEO")
	if url == "" || vid == "" {
		t.Skip("set FACE_SIDECAR_URL and FACE_TEST_VIDEO to run the live-frame test")
	}
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not on PATH")
	}

	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", vid,
		"-an", "-vf", "fps=2,scale=640:-2", "-f", "image2pipe", "-c:v", "mjpeg", "pipe:1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg mjpeg: %v", err)
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	sc.Split(splitMJPEG)
	var frames [][]byte
	for sc.Scan() {
		frames = append(frames, append([]byte(nil), sc.Bytes()...))
	}
	if len(frames) == 0 {
		t.Fatal("no frames carved from the mjpeg stream")
	}

	d := &LiveDetector{Client: NewClient(url), DetThreshold: 0.5}
	found := 0
	for _, f := range frames {
		if has, _ := d.frameHasFace(context.Background(), f, 0.5); has {
			found++
		}
	}
	t.Logf("live frames=%d withFace=%d", len(frames), found)
	if found == 0 {
		t.Error("expected at least one frame with a face")
	}
}
