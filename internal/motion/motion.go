package motion

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os/exec"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/core"
	"github.com/AkagiYui/kenko-nvr/internal/tsfeed"
)

// downscaled analysis frame geometry and sample rate. Tiny frames keep the diff
// cheap; 16:9 keeps aspect close to typical cameras.
const (
	frameW    = 64
	frameH    = 36
	frameRate = 4
	pixelDelta = 18
)

// Available reports whether FFmpeg (required for motion detection) is on PATH.
func Available() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// Detector runs motion detection on a live stream, invoking OnStart/OnEnd as
// motion begins and ends. It feeds the stream to FFmpeg as MPEG-TS and reads
// back tiny grayscale frames, which it diffs in Go.
type Detector struct {
	Source      *core.Stream
	Sensitivity int
	Cooldown    time.Duration // motion-end debounce; 0 -> 4s
	Log         *slog.Logger

	OnStart func(t time.Time)
	OnEnd   func(t time.Time, score float64)
}

// Run blocks until ctx is cancelled or the stream ends.
func (d *Detector) Run(ctx context.Context) error {
	cooldown := d.Cooldown
	if cooldown <= 0 {
		cooldown = 4 * time.Second
	}
	an := newAnalyzer(d.Sensitivity, cooldown)

	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "mpegts", "-i", "pipe:0",
		"-an",
		"-vf", "fps=4,scale=64:36,format=gray",
		"-f", "rawvideo", "pipe:1",
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...) //nolint:gosec // fixed args
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go drainLog(stderr, d.Log)

	// Feed the live stream into FFmpeg as MPEG-TS; closing stdin makes it exit.
	go func() {
		defer stdin.Close()
		_ = tsfeed.Feed(ctx, d.Source, stdin)
	}()

	d.analyze(an, bufio.NewReaderSize(stdout, frameW*frameH*4))

	// Close any in-progress event before returning.
	if ended, score := an.finish(); ended && d.OnEnd != nil {
		d.OnEnd(time.Now(), score)
	}
	_ = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// analyze reads fixed-size grayscale frames and drives the analyzer.
func (d *Detector) analyze(an *analyzer, r io.Reader) {
	const frameSize = frameW * frameH
	cur := make([]byte, frameSize)
	prev := make([]byte, frameSize)
	hasPrev := false
	for {
		if _, err := io.ReadFull(r, cur); err != nil {
			return
		}
		if !hasPrev {
			copy(prev, cur)
			hasPrev = true
			continue
		}
		ratio := changeRatio(prev, cur, pixelDelta)
		started, ended, score := an.update(ratio, time.Now())
		if started && d.OnStart != nil {
			d.OnStart(time.Now())
		}
		if ended && d.OnEnd != nil {
			d.OnEnd(time.Now(), score)
		}
		copy(prev, cur)
	}
}

func drainLog(rc io.Reader, log *slog.Logger) {
	sc := bufio.NewScanner(rc)
	for sc.Scan() {
		if log != nil {
			log.Debug("motion ffmpeg", "line", sc.Text())
		}
	}
}
