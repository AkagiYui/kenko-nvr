package face

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
)

// Frame is a sampled JPEG frame with its millisecond offset from the start of
// the recording it was decoded from.
type Frame struct {
	OffsetMS int64
	JPEG     []byte
}

// ExtractRange decodes the window [startSec, startSec+durSec) of an mp4 at fps
// frames/second into JPEGs, capped at maxFrames. durSec <= 0 means "to the end".
// scaleWidth > 0 downscales each frame to that width (keeping aspect, even
// height). It shells out to ffmpeg (the same dependency the rest of the NVR
// uses); pixel decoding can't happen in the CGO-free Go process.
//
// Frame i's offset is startSec + i/fps (the fps filter emits a constant rate),
// which the caller adds to the recording's wall-clock start to get an absolute
// instant.
func ExtractRange(ctx context.Context, ffmpegPath, path string, startSec, durSec, fps float64, maxFrames, scaleWidth int) ([]Frame, error) {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if fps <= 0 {
		fps = 2
	}
	dir, err := os.MkdirTemp("", "kenko-face-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	vf := fmt.Sprintf("fps=%s", trimFloat(fps))
	if scaleWidth > 0 {
		vf += fmt.Sprintf(",scale=%d:-2", scaleWidth)
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	// Input (-ss before -i) seeking is fast and accurate enough for sampling.
	if startSec > 0 {
		args = append(args, "-ss", trimFloat(startSec))
	}
	args = append(args, "-i", path)
	if durSec > 0 {
		args = append(args, "-t", trimFloat(durSec))
	}
	args = append(args, "-an", "-sn", "-vf", vf, "-q:v", "3")
	if maxFrames > 0 {
		args = append(args, "-frames:v", strconv.Itoa(maxFrames))
	}
	args = append(args, filepath.Join(dir, "f_%06d.jpg"))

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg extract: %w: %s", err, string(out))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	frames := make([]Frame, 0, len(names))
	for i, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		offsetMS := int64((startSec + float64(i)/fps) * 1000)
		frames = append(frames, Frame{OffsetMS: offsetMS, JPEG: data})
	}
	return frames, nil
}

// trimFloat formats a float for ffmpeg args without a trailing exponent.
func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 3, 64)
}
