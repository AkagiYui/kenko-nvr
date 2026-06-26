package transcode

import (
	"strings"
	"testing"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"

	"github.com/AkagiYui/kenko-nvr/internal/core"
	"github.com/AkagiYui/kenko-nvr/internal/hwaccel"
)

func TestFFmpegArgsWithAudio(t *testing.T) {
	tc := &LiveTranscoder{Encoder: hwaccel.Software(), Bitrate: 3000, GOP: 40}
	args := strings.Join(tc.ffmpegArgs(true), " ")
	for _, want := range []string{
		"-f mpegts -i pipe:0", // input
		"-c:v libx264",        // encode video
		"-g 40", "-b:v 3000k", // rate/gop
		"-c:a copy",        // passthrough audio
		"-f mpegts pipe:1", // output
	} {
		if !strings.Contains(args, want) {
			t.Errorf("ffmpegArgs missing %q in %q", want, args)
		}
	}
}

func TestFFmpegArgsNoAudio(t *testing.T) {
	tc := &LiveTranscoder{Encoder: hwaccel.Software()}
	args := strings.Join(tc.ffmpegArgs(false), " ")
	if !strings.Contains(args, "-an") {
		t.Errorf("ffmpegArgs(false) should disable audio with -an, got %q", args)
	}
	if strings.Contains(args, "-c:a") {
		t.Errorf("ffmpegArgs(false) should not configure an audio codec, got %q", args)
	}
}

func TestFFmpegArgsDefaults(t *testing.T) {
	// Bitrate/GOP unset -> defaults applied.
	tc := &LiveTranscoder{Encoder: hwaccel.Software()}
	args := strings.Join(tc.ffmpegArgs(true), " ")
	if !strings.Contains(args, "-b:v 2500k") || !strings.Contains(args, "-g 50") {
		t.Errorf("defaults not applied: %q", args)
	}
}

func TestExtractParamSets(t *testing.T) {
	sps := []byte{0x67, 0x42, 0x00, 0x1f} // NAL type 7
	pps := []byte{0x68, 0xce, 0x3c, 0x80} // NAL type 8
	idr := []byte{0x65, 0x88}             // NAL type 5

	// A keyframe AU carrying SPS+PPS+IDR yields both param sets.
	gotSPS, gotPPS := extractParamSets([][]byte{sps, pps, idr}, nil, nil)
	if string(gotSPS) != string(sps) || string(gotPPS) != string(pps) {
		t.Fatalf("extract from full AU: sps=%x pps=%x", gotSPS, gotPPS)
	}

	// A later AU without param sets preserves the previously seen values.
	gotSPS, gotPPS = extractParamSets([][]byte{idr}, sps, pps)
	if string(gotSPS) != string(sps) || string(gotPPS) != string(pps) {
		t.Fatalf("preserve previous: sps=%x pps=%x", gotSPS, gotPPS)
	}

	// SPS arriving before PPS: one stays nil until the other is seen.
	gotSPS, gotPPS = extractParamSets([][]byte{sps}, nil, nil)
	if gotSPS == nil || gotPPS != nil {
		t.Fatalf("partial: expected sps set, pps nil; got sps=%x pps=%x", gotSPS, gotPPS)
	}
}

func TestExtractParamSetsNALUTypes(t *testing.T) {
	// Guard against a regression in the 5-bit NAL type mask.
	if h264.NALUType(0x67&0x1f) != h264.NALUTypeSPS {
		t.Fatal("SPS NAL type mask wrong")
	}
	if h264.NALUType(0x68&0x1f) != h264.NALUTypePPS {
		t.Fatal("PPS NAL type mask wrong")
	}
}

func TestRescale(t *testing.T) {
	cases := []struct {
		v, from, to, want int64
	}{
		{90000, 90000, 90000, 90000}, // identity
		{90000, 90000, 48000, 48000}, // 1s at 90k -> 48k samples
		{0, 90000, 48000, 0},
		{180000, 90000, 48000, 96000},
		{1000, 0, 48000, 1000}, // guard against div-by-zero
	}
	for _, c := range cases {
		if got := rescale(c.v, c.from, c.to); got != c.want {
			t.Errorf("rescale(%d,%d,%d) = %d, want %d", c.v, c.from, c.to, got, c.want)
		}
	}
}

func TestBuildInputTracks(t *testing.T) {
	cfg := &mpeg4audio.AudioSpecificConfig{
		Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 48000, ChannelCount: 2,
	}
	tracks := []*core.Track{
		{ID: 1, Kind: core.MediaVideo, Codec: core.CodecH265, ClockRate: 90000},
		{ID: 2, Kind: core.MediaAudio, Codec: core.CodecAAC, ClockRate: 48000, AudioConfig: cfg},
	}
	byID, mts := buildInputTracks(tracks)
	if len(mts) != 2 {
		t.Fatalf("expected 2 mpegts tracks, got %d", len(mts))
	}
	if byID[1].h265 == nil {
		t.Error("H.265 track should have an H.265 DTS extractor")
	}
	if !byID[2].audio {
		t.Error("AAC track should be flagged as audio")
	}
}

func TestBuildInputTracksSkipsAudioWithoutConfig(t *testing.T) {
	tracks := []*core.Track{
		{ID: 1, Kind: core.MediaVideo, Codec: core.CodecH264, ClockRate: 90000},
		{ID: 2, Kind: core.MediaAudio, Codec: core.CodecAAC, ClockRate: 48000}, // no config
	}
	byID, mts := buildInputTracks(tracks)
	if len(mts) != 1 {
		t.Fatalf("AAC track without config must be skipped; got %d tracks", len(mts))
	}
	if _, ok := byID[2]; ok {
		t.Error("AAC track without config should not be registered")
	}
}
