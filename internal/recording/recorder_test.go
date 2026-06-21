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
}

func (f *fakeSink) SegmentStarted(_, rel string, _ time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	f.lastPath = rel
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
