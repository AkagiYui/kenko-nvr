package transcode

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts"
	mpegtscodecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts/codecs"

	"github.com/AkagiYui/kenko-nvr/internal/core"
	"github.com/AkagiYui/kenko-nvr/internal/hwaccel"
	"github.com/AkagiYui/kenko-nvr/internal/mse"
)

// TestLiveTranscodeEndToEnd drives the whole pipeline with real FFmpeg: it
// generates a short H.265 + AAC clip, replays it into a source core.Stream, runs
// the LiveTranscoder, and asserts that a browser-playable H.264 MSE stream comes
// out (init segment + at least one fragment). Skipped when FFmpeg is absent.
func TestLiveTranscodeEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}

	units, audioCfg := generateH265Clip(t)
	if len(units) == 0 {
		t.Fatal("no units captured from generated clip")
	}

	// Source stream: H.265 video (+ AAC audio). The feed path reads parameter
	// sets in-band from the AUs, so the track's SPS/PPS may stay nil here.
	srcTracks := []*core.Track{
		{ID: 1, Kind: core.MediaVideo, Codec: core.CodecH265, ClockRate: tsClockRate},
	}
	if audioCfg != nil {
		srcTracks = append(srcTracks, &core.Track{
			ID: 2, Kind: core.MediaAudio, Codec: core.CodecAAC,
			ClockRate: tsClockRate, AudioConfig: audioCfg,
		})
	}
	src := core.NewStream(srcTracks)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// Replay the captured units on a loop, offsetting timestamps each pass so the
	// transcoder sees a continuous, monotonically increasing stream.
	go replay(ctx, src, units)

	tc := &LiveTranscoder{Source: src, Encoder: hwaccel.Software(), Bitrate: 800, GOP: 15, Log: nil}
	defer tc.Close()

	out, err := tc.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer tc.Release()

	if v := out.VideoTrack(); v == nil || v.Codec != core.CodecH264 {
		t.Fatalf("transcoded video track = %v, want H264", v)
	}

	// The output must be valid MSE: an init segment, then a real fragment.
	frag := mse.NewFragmenter(out.Tracks())
	if _, err := frag.InitSegment(); err != nil {
		t.Fatalf("init segment from transcoded stream: %v", err)
	}

	reader := out.AddReader(1024)
	defer reader.Close()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for a transcoded MSE fragment")
		case u, ok := <-reader.Units():
			if !ok {
				t.Fatal("transcoded stream closed before producing a fragment")
			}
			out, err := frag.Push(u)
			if err != nil {
				t.Fatalf("fragmenting transcoded unit: %v", err)
			}
			if len(out) > 0 {
				t.Logf("got a %d-byte H.264 MSE fragment from the transcoder", len(out))
				return // success
			}
		}
	}
}

// TestSharedAcrossViewers verifies the core promise: many concurrent viewers are
// served by a single transcode (they receive the very same output stream), and
// once they all leave the transcoder tears down and a later viewer gets a fresh
// one. Skipped when FFmpeg is absent.
func TestSharedAcrossViewers(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	units, _ := generateH265Clip(t)

	src := core.NewStream([]*core.Track{
		{ID: 1, Kind: core.MediaVideo, Codec: core.CodecH265, ClockRate: tsClockRate},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	go replay(ctx, src, units)

	tc := &LiveTranscoder{Source: src, Encoder: hwaccel.Software(), GOP: 15, Grace: 150 * time.Millisecond}
	defer tc.Close()

	// Five viewers arrive concurrently.
	const n = 5
	streams := make([]*core.Stream, n)
	errs := make([]error, n)
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			streams[i], errs[i] = tc.Acquire(ctx)
			done <- i
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("viewer %d Acquire: %v", i, errs[i])
		}
		if streams[i] != streams[0] {
			t.Fatalf("viewer %d got a different stream — transcode is not shared", i)
		}
	}
	first := streams[0]

	// Everyone leaves; after the grace period the shared transcode stops.
	for i := 0; i < n; i++ {
		tc.Release()
	}

	// A viewer returning after teardown gets a freshly started transcode (a
	// different output stream). Poll briefly to allow the grace timer to fire.
	deadline := time.After(10 * time.Second)
	for {
		s, err := tc.Acquire(ctx)
		if err != nil {
			t.Fatalf("re-acquire after teardown: %v", err)
		}
		if s != first {
			tc.Release()
			return // success: a new transcode was started
		}
		tc.Release()
		select {
		case <-deadline:
			t.Fatal("transcoder never tore down after the last viewer left")
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// generateH265Clip uses FFmpeg to synthesize a 2 s H.265 + AAC MPEG-TS clip and
// returns its access units (90 kHz PTS) plus the AAC config.
func generateH265Clip(t *testing.T) ([]*core.Unit, *mpeg4audio.AudioSpecificConfig) {
	t.Helper()
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "2",
		"-c:v", "libx265", "-x265-params", "log-level=none", "-g", "15", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ar", "48000", "-ac", "2",
		"-f", "mpegts", "pipe:1",
	}
	cmd := exec.Command("ffmpeg", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generating clip: %v: %s", err, stderr.String())
	}

	reader, err := mpegts.NewReader(bytes.NewReader(stdout.Bytes()))
	if err != nil {
		t.Fatalf("reading generated clip: %v", err)
	}

	var (
		units    []*core.Unit
		audioCfg *mpeg4audio.AudioSpecificConfig
	)
	for _, tr := range reader.Tracks() {
		switch c := tr.Codec.(type) {
		case *mpegtscodecs.H265:
			tr := tr
			reader.OnDataH265(tr, func(pts, _ int64, au [][]byte) error {
				dup := make([][]byte, len(au))
				for i, n := range au {
					dup[i] = append([]byte(nil), n...)
				}
				units = append(units, &core.Unit{
					TrackID: 1, PTS: pts, RandomAccess: h265.IsRandomAccess(au), AUs: dup,
				})
				return nil
			})
		case *mpegtscodecs.MPEG4Audio:
			cfg := c.Config
			audioCfg = &cfg
			tr := tr
			reader.OnDataMPEG4Audio(tr, func(pts int64, aus [][]byte) error {
				dup := make([][]byte, len(aus))
				for i, n := range aus {
					dup[i] = append([]byte(nil), n...)
				}
				units = append(units, &core.Unit{TrackID: 2, PTS: pts, RandomAccess: true, AUs: dup})
				return nil
			})
		}
	}
	for {
		if err := reader.Read(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("demuxing generated clip: %v", err)
		}
	}
	return units, audioCfg
}

// replay writes the captured units into src on a loop until ctx is cancelled,
// shifting PTS forward each pass to keep timestamps monotonic, and pacing lightly
// so FFmpeg sees a steady feed rather than a single burst.
func replay(ctx context.Context, src *core.Stream, units []*core.Unit) {
	var maxPTS int64
	for _, u := range units {
		if u.PTS > maxPTS {
			maxPTS = u.PTS
		}
	}
	span := maxPTS + tsClockRate/15 // one extra frame so loops don't collide

	for off := int64(0); ctx.Err() == nil; off += span {
		for _, u := range units {
			if ctx.Err() != nil {
				return
			}
			cp := *u
			cp.PTS = u.PTS + off
			cp.NTP = time.Now()
			src.WriteUnit(&cp)
			if u.TrackID == 1 {
				time.Sleep(12 * time.Millisecond)
			}
		}
	}
}
