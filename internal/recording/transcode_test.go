package recording

import (
	"strings"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

func TestNextAlignedBoundary(t *testing.T) {
	loc := time.UTC
	d := func(h, m, s int) time.Time { return time.Date(2025, 6, 25, h, m, s, 0, loc) }
	cases := []struct {
		name string
		t    time.Time
		dur  time.Duration
		want time.Time
	}{
		{"mid-segment", d(10, 3, 47), 10 * time.Minute, d(10, 10, 0)},
		{"on-boundary advances", d(10, 10, 0), 10 * time.Minute, d(10, 20, 0)},
		{"hourly", d(10, 3, 47), time.Hour, d(11, 0, 0)},
		{"wraps to next midnight", d(23, 55, 0), 10 * time.Minute, time.Date(2025, 6, 26, 0, 0, 0, 0, loc)},
		{"non-divisor restarts at midnight", d(0, 5, 0), 7 * time.Minute, d(0, 7, 0)},
	}
	for _, c := range cases {
		if got := nextAlignedBoundary(c.t, c.dur); !got.Equal(c.want) {
			t.Errorf("%s: nextAlignedBoundary(%v,%v) = %v, want %v", c.name, c.t, c.dur, got, c.want)
		}
	}
	if got := nextAlignedBoundary(d(1, 0, 0), 0); !got.Equal(d(1, 0, 0)) {
		t.Errorf("zero duration should return t unchanged, got %v", got)
	}
}

func TestSegmentNameRoundTrip(t *testing.T) {
	want := time.Date(2025, 6, 25, 20, 10, 5, 0, time.Local)
	// FFmpeg's strftime output for this time, given segNamePattern.
	name := "seg-" + want.Format(segTimeLayout) + ".mp4"
	if name != "seg-20250625-201005.mp4" {
		t.Fatalf("unexpected segment name %q", name)
	}
	got, err := parseSegmentTime(name)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("round trip: got %v, want %v", got, want)
	}
}

func TestRescale(t *testing.T) {
	if got := rescale(100, 90000, 90000); got != 100 {
		t.Errorf("identity rescale = %d, want 100", got)
	}
	if got := rescale(44100, 44100, 90000); got != 90000 {
		t.Errorf("rescale(1s @44100 -> 90k) = %d, want 90000", got)
	}
	// large value must not overflow
	big := int64(44100) * 3600 * 24 * 30 // ~1 month of audio samples
	if got := rescale(big, 44100, 90000); got <= 0 {
		t.Errorf("rescale overflowed: %d", got)
	}
}

func TestBuildTSTracks(t *testing.T) {
	cfg := &mpeg4audio.AudioSpecificConfig{Type: 2, SampleRate: 44100, ChannelCount: 1, ChannelConfig: 1}
	tracks := []*core.Track{
		{ID: 1, Kind: core.MediaVideo, Codec: core.CodecH265, ClockRate: 90000, VPS: []byte{1}, SPS: []byte{2}, PPS: []byte{3}},
		{ID: 2, Kind: core.MediaAudio, Codec: core.CodecAAC, ClockRate: 44100, AudioConfig: cfg},
	}
	byID, mts := buildTSTracks(tracks)
	if len(mts) != 2 {
		t.Fatalf("expected 2 mpegts tracks, got %d", len(mts))
	}
	if byID[1] == nil || byID[1].h265 == nil {
		t.Errorf("video track missing or no H265 DTS extractor")
	}
	if byID[2] == nil || !byID[2].audio {
		t.Errorf("audio track missing or not marked audio")
	}
}

func TestFFmpegArgs(t *testing.T) {
	r := &TranscodeRecorder{Root: "/tmp/rec", SegmentDur: 600 * time.Second, VideoCodec: "h264", CRF: 23, Preset: "fast"}
	r.workDir = "/tmp/rec/.transcode/cam"
	args := strings.Join(r.ffmpegArgs(), " ")
	for _, want := range []string{
		"-f mpegts", "-i pipe:0", "-c:v libx264", "-crf 23", "-preset fast",
		"-segment_atclocktime 1", "-strftime 1", "-segment_list pipe:1",
		"-force_key_frames expr:gte(t,n_forced*600)", "/tmp/rec/.transcode/cam/seg-%Y%m%d-%H%M%S.mp4",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("ffmpeg args missing %q\n got: %s", want, args)
		}
	}
	// HEVC selects libx265 + hvc1 tag.
	r.VideoCodec = "hevc"
	args = strings.Join(r.ffmpegArgs(), " ")
	if !strings.Contains(args, "-c:v libx265") || !strings.Contains(args, "-tag:v hvc1") {
		t.Errorf("hevc args wrong: %s", args)
	}
}
