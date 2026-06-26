package restream

import (
	"bytes"
	"testing"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

func TestTagEncoding(t *testing.T) {
	data := []byte{0xAA, 0xBB}
	out := tag(tagVideo, 0x010203, data)

	if len(out) != 11+len(data)+4 {
		t.Fatalf("tag length = %d, want %d", len(out), 11+len(data)+4)
	}
	if out[0] != tagVideo {
		t.Errorf("tag type = %d, want %d", out[0], tagVideo)
	}
	// DataSize (3 bytes BE).
	if out[1] != 0 || out[2] != 0 || out[3] != 2 {
		t.Errorf("data size bytes = %v, want [0 0 2]", out[1:4])
	}
	// Timestamp: low 24 bits in [4:7], extended high byte in [7].
	if out[4] != 0x01 || out[5] != 0x02 || out[6] != 0x03 || out[7] != 0x00 {
		t.Errorf("timestamp bytes = %v", out[4:8])
	}
	if !bytes.Equal(out[11:13], data) {
		t.Errorf("payload mismatch: %v", out[11:13])
	}
	// Trailing PreviousTagSize = 11 + DataSize.
	prev := uint32(out[13])<<24 | uint32(out[14])<<16 | uint32(out[15])<<8 | uint32(out[16])
	if prev != 13 {
		t.Errorf("previous tag size = %d, want 13", prev)
	}
}

func TestAVCDecoderConfig(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xC0, 0x1F, 0xAA}
	pps := []byte{0x68, 0xCE, 0x3C}
	cfg := avcDecoderConfig(sps, pps)

	if cfg[0] != 0x01 {
		t.Errorf("configurationVersion = %d, want 1", cfg[0])
	}
	if cfg[1] != sps[1] || cfg[2] != sps[2] || cfg[3] != sps[3] {
		t.Errorf("profile/compat/level not taken from SPS: %v", cfg[1:4])
	}
	if cfg[4] != 0xFF || cfg[5] != 0xE1 {
		t.Errorf("lengthSize/numSPS bytes = %v, want [FF E1]", cfg[4:6])
	}
	// SPS length + body.
	spsLen := int(cfg[6])<<8 | int(cfg[7])
	if spsLen != len(sps) || !bytes.Equal(cfg[8:8+spsLen], sps) {
		t.Errorf("SPS not embedded correctly")
	}
}

func h264Track() *core.Track {
	return &core.Track{
		ID: 1, Kind: core.MediaVideo, Codec: core.CodecH264, ClockRate: 90000,
		SPS: []byte{0x67, 0x42, 0xC0, 0x1F}, PPS: []byte{0x68, 0xCE, 0x3C},
	}
}

func TestNewFlvMuxer(t *testing.T) {
	asc := &mpeg4audio.AudioSpecificConfig{
		Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 44100, ChannelCount: 2,
	}
	tracks := []*core.Track{
		h264Track(),
		{ID: 2, Kind: core.MediaAudio, Codec: core.CodecAAC, ClockRate: 44100, AudioConfig: asc},
	}
	m, err := NewFlvMuxer(tracks)
	if err != nil {
		t.Fatalf("NewFlvMuxer: %v", err)
	}
	hdr := m.Header()
	if string(hdr[:3]) != "FLV" || hdr[3] != 0x01 {
		t.Errorf("bad FLV signature: %v", hdr[:4])
	}
	if hdr[4]&0x01 == 0 || hdr[4]&0x04 == 0 {
		t.Errorf("FLV flags should advertise audio+video, got %#x", hdr[4])
	}
}

func TestNewFlvMuxerRejectsH265(t *testing.T) {
	tracks := []*core.Track{{ID: 1, Kind: core.MediaVideo, Codec: core.CodecH265, ClockRate: 90000}}
	if _, err := NewFlvMuxer(tracks); err == nil {
		t.Error("expected error for H.265 video (FLV cannot carry it)")
	}
	if _, err := NewFlvMuxer(nil); err == nil {
		t.Error("expected error when there is no video track")
	}
}
