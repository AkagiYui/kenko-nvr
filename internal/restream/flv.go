// Package restream re-publishes a live core.Stream in standard pull formats so
// external clients (browsers, players, other servers) can consume it: HTTP-FLV,
// WebSocket-FLV and HTTP-MPEG-TS. The FLV muxer here is pure (no I/O) so it can
// be unit-tested.
package restream

import (
	"encoding/binary"
	"fmt"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// FLV tag types.
const (
	tagAudio = 8
	tagVideo = 9
)

// FlvMuxer converts core.Units into an FLV byte stream: an FLV header, the
// AVC/AAC sequence headers, then one tag per access unit. It carries H.264 video
// and AAC audio (standard FLV); feed it a browser-playable (H.264) stream.
//
// It is not safe for concurrent use; drive it from one goroutine.
type FlvMuxer struct {
	video *core.Track
	audio *core.Track
	h264  *h264.DTSExtractor

	baseVideoMS int64
	baseAudioMS int64
	hasBaseV    bool
	hasBaseA    bool
	wroteVSeq   bool
	wroteASeq   bool
}

// NewFlvMuxer builds a muxer for the stream's tracks. It returns an error if the
// video is not H.264 (FLV cannot carry H.265).
func NewFlvMuxer(tracks []*core.Track) (*FlvMuxer, error) {
	m := &FlvMuxer{}
	for _, t := range tracks {
		switch {
		case t.IsVideo() && m.video == nil:
			if t.Codec != core.CodecH264 {
				return nil, fmt.Errorf("flv requires H.264 video, got %s", t.Codec)
			}
			m.video = t
			m.h264 = h264.NewDTSExtractor()
		case !t.IsVideo() && t.Codec == core.CodecAAC && m.audio == nil:
			m.audio = t
		}
	}
	if m.video == nil {
		return nil, fmt.Errorf("flv requires a video track")
	}
	return m, nil
}

// Header returns the FLV file header plus the first PreviousTagSize and the
// AVC/AAC sequence headers. Send it once before any Push output.
func (m *FlvMuxer) Header() []byte {
	flags := byte(0x01) // video
	if m.audio != nil {
		flags |= 0x04 // audio
	}
	out := []byte{'F', 'L', 'V', 0x01, flags, 0, 0, 0, 9, 0, 0, 0, 0}

	// AVC sequence header (avcC) at timestamp 0.
	avcc := avcDecoderConfig(m.video.SPS, m.video.PPS)
	vbody := append([]byte{0x17, 0x00, 0, 0, 0}, avcc...) // keyframe|AVC, seqhdr, cts=0
	out = append(out, tag(tagVideo, 0, vbody)...)
	m.wroteVSeq = true

	if m.audio != nil {
		asc := m.audio.AudioConfig
		if asc != nil {
			data, err := asc.Marshal()
			if err == nil {
				abody := append([]byte{0xAF, 0x00}, data...) // AAC seqhdr
				out = append(out, tag(tagAudio, 0, abody)...)
				m.wroteASeq = true
			}
		}
	}
	return out
}

// Push returns the FLV tag(s) for one unit, or nil if the unit is not yet
// usable (e.g. video before decode order is established).
func (m *FlvMuxer) Push(u *core.Unit) ([]byte, error) {
	switch {
	case m.video != nil && u.TrackID == m.video.ID:
		return m.pushVideo(u)
	case m.audio != nil && u.TrackID == m.audio.ID:
		return m.pushAudio(u)
	default:
		return nil, nil
	}
}

func (m *FlvMuxer) pushVideo(u *core.Unit) ([]byte, error) {
	dts, err := m.h264.Extract(u.AUs, u.PTS)
	if err != nil {
		return nil, nil // decode order not established yet
	}
	clock := int64(m.video.ClockRate)
	dtsMS := dts * 1000 / clock
	if !m.hasBaseV {
		m.baseVideoMS = dtsMS
		m.hasBaseV = true
	}
	ts := dtsMS - m.baseVideoMS
	cts := (u.PTS - dts) * 1000 / clock

	payload, err := h264.AVCC(u.AUs).Marshal()
	if err != nil {
		return nil, err
	}
	frameType := byte(2) // inter
	if u.RandomAccess {
		frameType = 1
	}
	body := make([]byte, 5, 5+len(payload))
	body[0] = (frameType << 4) | 0x07 // AVC
	body[1] = 0x01                    // NALU
	body[2] = byte(cts >> 16)
	body[3] = byte(cts >> 8)
	body[4] = byte(cts)
	body = append(body, payload...)
	return tag(tagVideo, uint32(clampTS(ts)), body), nil
}

func (m *FlvMuxer) pushAudio(u *core.Unit) ([]byte, error) {
	if !m.wroteASeq {
		return nil, nil
	}
	clock := int64(m.audio.ClockRate)
	var out []byte
	for i, frame := range u.AUs {
		ptsMS := (u.PTS + int64(i*1024)) * 1000 / clock
		if !m.hasBaseA {
			m.baseAudioMS = ptsMS
			m.hasBaseA = true
		}
		ts := ptsMS - m.baseAudioMS
		body := make([]byte, 2, 2+len(frame))
		body[0] = 0xAF // AAC, 44kHz, 16-bit, stereo (rate/size/type ignored for AAC)
		body[1] = 0x01 // raw
		body = append(body, frame...)
		out = append(out, tag(tagAudio, uint32(clampTS(ts)), body)...)
	}
	return out, nil
}

func clampTS(ts int64) int64 {
	if ts < 0 {
		return 0
	}
	return ts
}

// tag encodes one FLV tag with its trailing PreviousTagSize.
func tag(tagType byte, timestampMS uint32, data []byte) []byte {
	size := len(data)
	out := make([]byte, 11+size+4)
	out[0] = tagType
	out[1] = byte(size >> 16)
	out[2] = byte(size >> 8)
	out[3] = byte(size)
	out[4] = byte(timestampMS >> 16)
	out[5] = byte(timestampMS >> 8)
	out[6] = byte(timestampMS)
	out[7] = byte(timestampMS >> 24) // extended high byte
	// bytes 8-10 (StreamID) stay zero.
	copy(out[11:], data)
	binary.BigEndian.PutUint32(out[11+size:], uint32(11+size))
	return out
}

// avcDecoderConfig builds an AVCDecoderConfigurationRecord (avcC) from one SPS
// and one PPS.
func avcDecoderConfig(sps, pps []byte) []byte {
	out := make([]byte, 0, 11+len(sps)+len(pps))
	profile, compat, level := byte(0x42), byte(0x00), byte(0x1f)
	if len(sps) >= 4 {
		profile, compat, level = sps[1], sps[2], sps[3]
	}
	out = append(out, 0x01, profile, compat, level, 0xFF, 0xE1)
	out = append(out, byte(len(sps)>>8), byte(len(sps)))
	out = append(out, sps...)
	out = append(out, 0x01)
	out = append(out, byte(len(pps)>>8), byte(len(pps)))
	out = append(out, pps...)
	return out
}
