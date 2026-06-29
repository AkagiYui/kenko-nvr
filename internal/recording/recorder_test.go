package recording

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

type fakeSink struct {
	mu        sync.Mutex
	started   int
	finalized int
	lastPath  string
	lastSize  int64
	starts    []time.Time
}

func (f *fakeSink) SegmentStarted(_, rel string, start time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	f.lastPath = rel
	f.starts = append(f.starts, start)
	return "rec-id", nil
}

func (f *fakeSink) SegmentFinalized(_ string, _ time.Time, _, size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalized++
	f.lastSize = size
	return nil
}

// TestRecorderAudioOnly drives the recorder through the audio path (which needs
// no SPS/DTS extraction) and verifies a real fMP4 file with a valid box layout
// is produced and finalized.
func TestRecorderAudioOnly(t *testing.T) {
	root := t.TempDir()
	cfg := &mpeg4audio.AudioSpecificConfig{
		Type:          2, // AAC-LC
		SampleRate:    44100,
		ChannelCount:  1,
		ChannelConfig: 1,
	}
	track := &core.Track{ID: 1, Kind: core.MediaAudio, Codec: core.CodecAAC, ClockRate: 44100, AudioConfig: cfg}
	stream := core.NewStream([]*core.Track{track})

	sink := &fakeSink{}
	rec := &Recorder{
		CameraID:   "cam",
		CameraName: "Cam",
		Root:       root,
		SegmentDur: time.Hour, // no time-based rotation in this test
		Template:   "{camera}/{unix}.mp4",
		Sink:       sink,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rec.Run(context.Background(), stream)
	}()

	time.Sleep(50 * time.Millisecond) // let Run attach its reader

	// One AAC frame ~= 1024 samples; feed enough to trigger a flush.
	frame := make([]byte, 64)
	for i := 0; i < 60; i++ {
		stream.WriteUnit(&core.Unit{
			TrackID: 1,
			PTS:     int64(i * 1024),
			NTP:     time.Now(),
			AUs:     [][]byte{frame},
		})
	}
	time.Sleep(100 * time.Millisecond)
	stream.Close()
	<-done

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.started != 1 {
		t.Fatalf("expected 1 segment started, got %d", sink.started)
	}
	if sink.finalized != 1 {
		t.Fatalf("expected 1 segment finalized, got %d", sink.finalized)
	}
	abs := filepath.Join(root, filepath.FromSlash(sink.lastPath))
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("recording file missing: %v", err)
	}
	if info.Size() == 0 || sink.lastSize == 0 {
		t.Fatalf("recording file is empty (size=%d, reported=%d)", info.Size(), sink.lastSize)
	}
	assertFMP4(t, abs)
}

// TestRecorderMotionPreRoll drives the gated (motion) recorder white-box through
// handle() with an injected clock, verifying that while gated off it buffers a
// bounded ring of recent GOPs and that, when the gate opens, the new segment is
// timestamped from before the trigger (the pre-roll) rather than at the trigger.
func TestRecorderMotionPreRoll(t *testing.T) {
	// A non-H.264/H.265 video codec skips DTS extraction (dts = PTS) so we can
	// feed synthetic access units; it cannot be muxed, so the fragment write at
	// activation fails — but only after the segment start time is recorded.
	track := &core.Track{ID: 1, Kind: core.MediaVideo, Codec: core.Codec("X"), ClockRate: 90000}

	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var clk time.Time
	gateOpen := false
	sink := &fakeSink{}
	rec := &Recorder{
		CameraID:   "cam",
		Root:       t.TempDir(),
		Template:   "{camera}/{unix}.mp4",
		SegmentDur: time.Hour,
		PreRoll:    5 * time.Second,
		Gate:       func(time.Time) bool { return gateOpen },
		Clock:      func() time.Time { return clk },
		Sink:       sink,
	}
	rec.initTracks([]*core.Track{track})

	const gopStep = 6000 // PTS per GOP (90kHz)
	feedGOP := func(g int) error {
		clk = base.Add(time.Duration(g) * 2 * time.Second) // 2s of wall time per GOP
		pts := int64(g * gopStep)
		if err := rec.handle(&core.Unit{TrackID: 1, PTS: pts, RandomAccess: true, AUs: [][]byte{{0x1}}}); err != nil {
			return err
		}
		// Two inter frames complete the GOP.
		if err := rec.handle(&core.Unit{TrackID: 1, PTS: pts + 2000, AUs: [][]byte{{0x2}}}); err != nil {
			return err
		}
		return rec.handle(&core.Unit{TrackID: 1, PTS: pts + 4000, AUs: [][]byte{{0x3}}})
	}

	// Feed GOPs 0..5 (keyframes at wall 0,2,4,6,8,10s) while gated off.
	for g := 0; g <= 5; g++ {
		if err := feedGOP(g); err != nil {
			t.Fatalf("feed GOP %d: %v", g, err)
		}
	}

	// Inter frames must be buffered while gated (a keyframe alone is unplayable).
	if got := len(rec.trackStates[1].pending); got != 3 {
		t.Fatalf("expected 3 buffered samples in the open GOP, got %d", got)
	}
	// Nothing should have been written yet.
	if sink.started != 0 || rec.writer != nil {
		t.Fatalf("recording started while gated off (started=%d)", sink.started)
	}
	// The ring must be pruned to ~PreRoll: the oldest kept GOP is at least PreRoll
	// (and at most PreRoll + one GOP) before the newest buffered GOP, so the
	// window covers the lead-up without growing unbounded.
	old := rec.oldestGOP()
	if old == nil {
		t.Fatal("expected buffered pre-roll GOPs, found none")
	}
	newestCached := rec.gopCache[len(rec.gopCache)-1].wall
	if span := newestCached.Sub(old.wall); span < 5*time.Second || span > 7*time.Second {
		t.Fatalf("pre-roll window out of bounds: span %v (want 5s..7s)", span)
	}

	// Open the gate and feed the trigger GOP at wall 12s.
	gateOpen = true
	// The fragment write fails (fake codec can't be muxed) but the segment is
	// opened and its start time recorded first; that is what we assert on.
	_ = feedGOP(6)

	if sink.started != 1 {
		t.Fatalf("expected the segment to open when the gate opened, started=%d", sink.started)
	}
	// The segment must start before the trigger (wall 12s) — i.e. pre-roll was
	// prepended — by at least PreRoll and at most PreRoll + two GOPs.
	trigger := base.Add(12 * time.Second)
	lead := trigger.Sub(sink.starts[0])
	if lead < 5*time.Second || lead > 9*time.Second {
		t.Fatalf("pre-roll lead = %v, want 5s..9s before the trigger", lead)
	}
}

// assertFMP4 checks the file begins with an init segment (ftyp+moov) and has at
// least one fragment (moof+mdat).
func assertFMP4(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var boxes []string
	for i := 0; i+8 <= len(data); {
		size := int(data[i])<<24 | int(data[i+1])<<16 | int(data[i+2])<<8 | int(data[i+3])
		boxes = append(boxes, string(data[i+4:i+8]))
		if size < 8 {
			break
		}
		i += size
	}
	has := func(b string) bool {
		for _, x := range boxes {
			if x == b {
				return true
			}
		}
		return false
	}
	if !has("ftyp") || !has("moov") {
		t.Errorf("missing init segment boxes; got %v", boxes)
	}
	if !has("moof") || !has("mdat") {
		t.Errorf("missing fragment boxes; got %v", boxes)
	}
}
