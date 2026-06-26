// Package transcode provides an on-demand, viewer-shared live transcoder.
//
// It re-encodes a camera's live core.Stream (typically H.265, which most
// browsers cannot play) into a browser-friendly H.264 core.Stream using a single
// FFmpeg process — regardless of how many people are watching. The process is
// started on the first viewer and stopped shortly after the last one leaves, and
// it is fed from the already-in-memory source stream, so the camera is never
// pulled a second time. The transcoded output is itself a core.Stream, so it
// fans out to N viewers through the same publish/subscribe hub as a native feed.
//
// Pipeline: source Units -> (MPEG-TS) -> FFmpeg encode -> (MPEG-TS) -> demux back
// into Units -> output core.Stream. Video is decoded in software and encoded by
// the resolved hwaccel.Encoder; audio (always AAC inside core) is stream-copied.
package transcode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts"
	mpegtscodecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts/codecs"

	"github.com/AkagiYui/kenko-nvr/internal/core"
	"github.com/AkagiYui/kenko-nvr/internal/hwaccel"
)

// tsClockRate is the 90 kHz clock used by MPEG-TS timestamps.
const tsClockRate = 90000

// defaults for the transcoded live profile.
const (
	defaultBitrateKbps = 2500
	defaultGOP         = 50
	defaultGrace       = 8 * time.Second
	startTimeout       = 20 * time.Second
)

// LiveTranscoder owns one shared FFmpeg transcode of a single source stream.
// Acquire/Release reference-count viewers; the FFmpeg process exists only while
// at least one viewer is attached (plus a short grace period).
type LiveTranscoder struct {
	Source  *core.Stream
	Encoder *hwaccel.Encoder
	Bitrate int           // kbit/s; 0 -> default
	GOP     int           // frames; 0 -> default
	Grace   time.Duration // keep FFmpeg alive this long after the last viewer; 0 -> default
	Log     *slog.Logger

	mu       sync.Mutex
	refs     int
	out      *core.Stream
	startErr error
	running  bool
	ready    chan struct{} // closed when the current run becomes ready or fails
	cancel   context.CancelFunc
	timer    *time.Timer
}

// Acquire registers a viewer and returns the shared transcoded stream, starting
// FFmpeg if this is the first viewer. The caller must invoke Release exactly once
// when done. The returned stream is closed when the last viewer releases (after
// the grace period), when the source ends, or on Close.
func (t *LiveTranscoder) Acquire(ctx context.Context) (*core.Stream, error) {
	t.mu.Lock()
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	t.refs++
	if t.out != nil {
		out := t.out
		t.mu.Unlock()
		return out, nil
	}
	if !t.running {
		t.running = true
		t.startErr = nil
		rctx, cancel := context.WithCancel(context.Background())
		t.cancel = cancel
		ch := make(chan struct{})
		t.ready = ch
		go t.run(rctx, ch)
	}
	ready := t.ready
	t.mu.Unlock()

	select {
	case <-ready:
	case <-ctx.Done():
		t.Release()
		return nil, ctx.Err()
	case <-time.After(startTimeout):
		t.Release()
		return nil, errors.New("transcode start timed out")
	}

	t.mu.Lock()
	out, err := t.out, t.startErr
	t.mu.Unlock()
	if out == nil {
		t.Release()
		if err == nil {
			err = errors.New("transcode failed to start")
		}
		return nil, err
	}
	return out, nil
}

// Release detaches a viewer. When the last viewer leaves, FFmpeg is stopped after
// the grace period (so a viewer who reconnects within it reuses the running
// process).
func (t *LiveTranscoder) Release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refs == 0 {
		return
	}
	t.refs--
	if t.refs > 0 {
		return
	}
	grace := t.Grace
	if grace <= 0 {
		grace = defaultGrace
	}
	if t.timer != nil {
		t.timer.Stop()
	}
	t.timer = time.AfterFunc(grace, t.stopIfIdle)
}

// Close stops the transcoder and its FFmpeg process immediately. It is safe to
// call multiple times.
func (t *LiveTranscoder) Close() {
	t.mu.Lock()
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	cancel := t.cancel
	t.cancel = nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (t *LiveTranscoder) stopIfIdle() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refs > 0 {
		return
	}
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
}

// run drives one FFmpeg lifetime and resets state when it ends so a later
// Acquire can start a fresh process. ready (captured per run, closed at most once
// by the local sync.Once) wakes the startup waiters in Acquire.
func (t *LiveTranscoder) run(ctx context.Context, ready chan struct{}) {
	var once sync.Once
	signal := func() { once.Do(func() { close(ready) }) }

	err := t.pump(ctx, func(s *core.Stream) {
		t.mu.Lock()
		t.out = s
		t.mu.Unlock()
		signal()
	})

	t.mu.Lock()
	if t.startErr == nil {
		t.startErr = err
	}
	if t.out != nil {
		t.out.Close()
		t.out = nil
	}
	t.running = false
	t.cancel = nil
	t.mu.Unlock()
	signal() // wake any startup waiter on the failure path

	if err != nil && ctx.Err() == nil && t.Log != nil {
		t.Log.Warn("live transcode stopped", "err", err)
	}
}

// pump runs FFmpeg, feeds it the source stream as MPEG-TS, and demuxes its
// MPEG-TS output back into a freshly built core.Stream. onReady is called once,
// when the output stream's tracks are known (at the first keyframe).
func (t *LiveTranscoder) pump(ctx context.Context, onReady func(*core.Stream)) error {
	srcByID, srcTSTracks := buildInputTracks(t.Source.Tracks())
	if len(srcTSTracks) == 0 {
		return errors.New("source has no transcodable tracks")
	}
	hasAudio := false
	for _, tt := range srcByID {
		if tt.audio {
			hasAudio = true
		}
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", t.ffmpegArgs(hasAudio)...) //nolint:gosec // args from config, not user input
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
		return fmt.Errorf("starting ffmpeg: %w", err)
	}
	go t.logStderr(stderr)

	// Force-kill backstop: if FFmpeg ignores stdin close on shutdown, kill it.
	// done is closed (via defer) once cmd.Wait has returned, so the backstop never
	// races os/exec's own child reaping.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			_ = cmd.Process.Kill()
		case <-done:
		}
	}()

	// Feed the live source into FFmpeg as MPEG-TS. Closing stdin (on ctx cancel or
	// source end) makes FFmpeg flush and exit.
	go func() {
		defer stdin.Close()
		w := mpegts.NewWriter(stdin, srcTSTracks)
		reader := t.Source.AddReader(1024)
		defer reader.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case u, ok := <-reader.Units():
				if !ok {
					return
				}
				if err := writeInputUnit(w, srcByID[u.TrackID], u); err != nil {
					return // FFmpeg exited / pipe broke
				}
			}
		}
	}()

	readErr := t.demux(bufio.NewReaderSize(stdout, 64*1024), hasAudio, onReady)

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil // normal shutdown
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	return waitErr
}

// demux reads FFmpeg's MPEG-TS output and republishes it into a core.Stream.
// The output stream is built lazily at the first keyframe (when in-band H.264
// SPS/PPS become available), then onReady is fired and Units are written.
func (t *LiveTranscoder) demux(r io.Reader, hasAudio bool, onReady func(*core.Stream)) error {
	reader, err := mpegts.NewReader(r)
	if err != nil {
		return fmt.Errorf("reading transcoded stream: %w", err)
	}

	var videoTS *mpegts.Track
	var audioCfg *mpeg4audio.AudioSpecificConfig
	for _, tr := range reader.Tracks() {
		switch c := tr.Codec.(type) {
		case *mpegtscodecs.H264:
			videoTS = tr
		case *mpegtscodecs.MPEG4Audio:
			cfg := c.Config
			audioCfg = &cfg
		}
	}
	if videoTS == nil {
		return errors.New("transcoded stream has no H.264 video")
	}
	if !hasAudio {
		audioCfg = nil
	}

	const (
		videoTrackID = 1
		audioTrackID = 2
	)
	var (
		out      *core.Stream
		sps, pps []byte
		audioHz  int
	)
	if audioCfg != nil {
		audioHz = audioCfg.SampleRate
	}

	ensureReady := func(au [][]byte) bool {
		if out != nil {
			return true
		}
		if s, p := extractParamSets(au, sps, pps); s != nil && p != nil {
			sps, pps = s, p
		} else {
			sps, pps = s, p
			return false // wait for a keyframe carrying both SPS and PPS
		}
		tracks := []*core.Track{{
			ID: videoTrackID, Kind: core.MediaVideo, Codec: core.CodecH264,
			ClockRate: tsClockRate, SPS: sps, PPS: pps,
		}}
		if audioCfg != nil {
			tracks = append(tracks, &core.Track{
				ID: audioTrackID, Kind: core.MediaAudio, Codec: core.CodecAAC,
				ClockRate: audioHz, AudioConfig: audioCfg,
			})
		}
		out = core.NewStream(tracks)
		onReady(out)
		return true
	}

	reader.OnDataH264(videoTS, func(pts, _ int64, au [][]byte) error {
		if !ensureReady(au) {
			return nil
		}
		out.WriteUnit(&core.Unit{
			TrackID:      videoTrackID,
			PTS:          pts,
			NTP:          time.Now(),
			RandomAccess: h264.IsRandomAccess(au),
			AUs:          au,
		})
		return nil
	})

	if audioCfg != nil {
		var audioTS *mpegts.Track
		for _, tr := range reader.Tracks() {
			if _, ok := tr.Codec.(*mpegtscodecs.MPEG4Audio); ok {
				audioTS = tr
			}
		}
		reader.OnDataMPEG4Audio(audioTS, func(pts int64, aus [][]byte) error {
			if out == nil {
				return nil // drop audio that precedes the first video keyframe
			}
			out.WriteUnit(&core.Unit{
				TrackID:      audioTrackID,
				PTS:          rescale(pts, tsClockRate, int64(audioHz)),
				NTP:          time.Now(),
				RandomAccess: true,
				AUs:          aus,
			})
			return nil
		})
	}

	for {
		if err := reader.Read(); err != nil {
			return err
		}
	}
}

// ffmpegArgs builds the transcode command: MPEG-TS in on stdin, H.264 (via the
// resolved encoder) + copied AAC out as MPEG-TS on stdout.
func (t *LiveTranscoder) ffmpegArgs(hasAudio bool) []string {
	bitrate := t.Bitrate
	if bitrate <= 0 {
		bitrate = defaultBitrateKbps
	}
	gop := t.GOP
	if gop <= 0 {
		gop = defaultGOP
	}

	a := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-fflags", "+genpts"}
	a = append(a, t.Encoder.InputArgs(false)...) // software decode + device init
	a = append(a, "-f", "mpegts", "-i", "pipe:0")
	a = append(a, t.Encoder.VideoArgs(bitrate, gop)...)
	if hasAudio {
		a = append(a, "-c:a", "copy") // core audio is always AAC; pass it through
	} else {
		a = append(a, "-an")
	}
	a = append(a, "-f", "mpegts", "pipe:1")
	return a
}

func (t *LiveTranscoder) logStderr(rc io.Reader) {
	sc := bufio.NewScanner(rc)
	for sc.Scan() {
		if t.Log != nil {
			t.Log.Debug("ffmpeg", "line", sc.Text())
		}
	}
}

// extractParamSets returns the SPS/PPS found in an access unit, preserving any
// previously seen value when this AU omits one.
func extractParamSets(au [][]byte, prevSPS, prevPPS []byte) (sps, pps []byte) {
	sps, pps = prevSPS, prevPPS
	for _, nal := range au {
		if len(nal) == 0 {
			continue
		}
		switch h264.NALUType(nal[0] & 0x1f) {
		case h264.NALUTypeSPS:
			sps = nal
		case h264.NALUTypePPS:
			pps = nal
		}
	}
	return sps, pps
}

// rescale converts a timestamp between clock rates without overflowing int64.
func rescale(v, from, to int64) int64 {
	if from == to || from == 0 {
		return v
	}
	return v/from*to + (v%from)*to/from
}

// inputTrack pairs a source core.Track with its MPEG-TS track and DTS extractor
// for feeding FFmpeg's stdin.
type inputTrack struct {
	core  *core.Track
	mts   *mpegts.Track
	h264  *h264.DTSExtractor
	h265  *h265.DTSExtractor
	audio bool
}

// buildInputTracks maps source tracks to MPEG-TS tracks and an id lookup, used
// to write the live source into FFmpeg as MPEG-TS.
func buildInputTracks(tracks []*core.Track) (map[int]*inputTrack, []*mpegts.Track) {
	byID := make(map[int]*inputTrack, len(tracks))
	var mtsTracks []*mpegts.Track
	for _, tr := range tracks {
		var codec mpegtscodecs.Codec
		it := &inputTrack{core: tr}
		switch tr.Codec {
		case core.CodecH264:
			codec = &mpegtscodecs.H264{}
			it.h264 = h264.NewDTSExtractor()
		case core.CodecH265:
			codec = &mpegtscodecs.H265{}
			it.h265 = h265.NewDTSExtractor()
		case core.CodecAAC:
			if tr.AudioConfig == nil {
				continue
			}
			codec = &mpegtscodecs.MPEG4Audio{Config: *tr.AudioConfig}
			it.audio = true
		default:
			continue
		}
		it.mts = &mpegts.Track{Codec: codec}
		byID[tr.ID] = it
		mtsTracks = append(mtsTracks, it.mts)
	}
	return byID, mtsTracks
}

func writeInputUnit(w *mpegts.Writer, it *inputTrack, u *core.Unit) error {
	if it == nil {
		return nil
	}
	pts := rescale(u.PTS, int64(it.core.ClockRate), tsClockRate)
	switch {
	case it.h264 != nil:
		dts, err := it.h264.Extract(u.AUs, u.PTS)
		if err != nil {
			return nil // decode order not established yet
		}
		return w.WriteH264(it.mts, pts, rescale(dts, int64(it.core.ClockRate), tsClockRate), u.AUs)
	case it.h265 != nil:
		dts, err := it.h265.Extract(u.AUs, u.PTS)
		if err != nil {
			return nil
		}
		return w.WriteH265(it.mts, pts, rescale(dts, int64(it.core.ClockRate), tsClockRate), u.AUs)
	default:
		return w.WriteMPEG4Audio(it.mts, pts, u.AUs)
	}
}
