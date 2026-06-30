package face

import (
	"bufio"
	"context"
	"log/slog"
	"math"
	"os/exec"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/core"
	"github.com/AkagiYui/kenko-nvr/internal/tsfeed"
)

// LiveDetector runs realtime face-presence detection on a live stream: it feeds
// the stream to ffmpeg as MPEG-TS, reads back sampled JPEG frames, asks the
// sidecar whether each contains a face, and raises OnStart/OnEnd as faces appear
// and clear (debounced by a cooldown), mirroring the motion detector. It does
// not store faces or identities — that stays with the post-process pipeline; this
// is purely for live alerts.
type LiveDetector struct {
	Source       *core.Stream
	Client       *Client
	SampleFPS    float64
	MinFaceSize  int
	DetThreshold float64
	Cooldown     time.Duration // face-clear debounce; 0 -> 4s
	Log          *slog.Logger

	OnStart func(t time.Time)
	OnEnd   func(t time.Time, score float64)
}

// Run blocks until ctx is cancelled or the stream ends.
func (d *LiveDetector) Run(ctx context.Context) error {
	fps := d.SampleFPS
	if fps <= 0 {
		fps = 1
	}
	cooldown := d.Cooldown
	if cooldown <= 0 {
		cooldown = 4 * time.Second
	}
	detThr := d.DetThreshold
	if detThr <= 0 {
		detThr = 0.5
	}

	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "mpegts", "-i", "pipe:0",
		"-an",
		"-vf", "fps=" + trimFloat(fps) + ",scale=640:-2",
		"-f", "image2pipe", "-c:v", "mjpeg", "pipe:1",
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
	go drainLiveLog(stderr, d.Log)
	go func() {
		defer stdin.Close()
		_ = tsfeed.Feed(ctx, d.Source, stdin)
	}()

	pres := &presence{cooldown: cooldown}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20) // JPEG frames can be large
	sc.Split(splitMJPEG)

	for sc.Scan() {
		if ctx.Err() != nil {
			break
		}
		frame := append([]byte(nil), sc.Bytes()...)
		has, score := d.frameHasFace(ctx, frame, detThr)
		started, ended, sd := pres.update(has, score, time.Now())
		if started && d.OnStart != nil {
			d.OnStart(time.Now())
		}
		if ended && d.OnEnd != nil {
			d.OnEnd(time.Now(), sd)
		}
	}

	if pres.active && d.OnEnd != nil {
		d.OnEnd(time.Now(), pres.best)
	}
	_ = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// frameHasFace asks the sidecar to analyse one frame and reports whether it
// holds a face passing the score/size gates, plus the best score.
func (d *LiveDetector) frameHasFace(ctx context.Context, jpeg []byte, detThr float64) (bool, float64) {
	dets, _, _, err := d.Client.Analyze(ctx, [][]byte{jpeg}, d.MinFaceSize)
	if err != nil || len(dets) == 0 {
		if err != nil && d.Log != nil {
			d.Log.Debug("face: live analyze error", "err", err)
		}
		return false, 0
	}
	best := 0.0
	has := false
	for _, f := range dets[0] {
		if f.DetScore < detThr {
			continue
		}
		w := f.BBox[2] - f.BBox[0]
		h := f.BBox[3] - f.BBox[1]
		if d.MinFaceSize > 0 && math.Min(w, h) < float64(d.MinFaceSize) {
			continue
		}
		has = true
		if f.DetScore > best {
			best = f.DetScore
		}
	}
	return has, best
}

// presence debounces face-present/absent transitions: it raises a start when a
// face first appears and an end once no face has been seen for the cooldown.
type presence struct {
	cooldown time.Duration
	active   bool
	lastSeen time.Time
	best     float64
}

func (p *presence) update(hasFace bool, score float64, now time.Time) (started, ended bool, sc float64) {
	if hasFace {
		p.lastSeen = now
		if score > p.best {
			p.best = score
		}
		if !p.active {
			p.active = true
			return true, false, score
		}
		return false, false, 0
	}
	if p.active && now.Sub(p.lastSeen) >= p.cooldown {
		p.active = false
		sc = p.best
		p.best = 0
		return false, true, sc
	}
	return false, false, 0
}

// splitMJPEG is a bufio.SplitFunc that yields one JPEG per token from a
// concatenated MJPEG stream, splitting on the EOI marker (FF D9). ffmpeg
// byte-stuffs FF in entropy data as FF 00, so a bare FF D9 only marks end-of-image.
func splitMJPEG(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 1; i < len(data); i++ {
		if data[i-1] == 0xFF && data[i] == 0xD9 {
			return i + 1, data[:i+1], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func drainLiveLog(rc interface{ Read([]byte) (int, error) }, log *slog.Logger) {
	sc := bufio.NewScanner(rc)
	for sc.Scan() {
		if log != nil {
			log.Debug("face live ffmpeg", "line", sc.Text())
		}
	}
}
