// Package mse turns a live core.Stream into a single-connection, low-latency
// feed for the browser's Media Source Extensions: one fragmented-MP4 init
// segment followed by one fMP4 fragment (moof+mdat) per video GOP. Unlike HLS
// there is no playlist and no per-segment polling — the client opens one
// WebSocket and the server pushes fragments as they are produced.
package mse

import (
	"fmt"
	"strings"

	"github.com/bluenviron/gohlslib/v2/pkg/codecparams"
	ghcodecs "github.com/bluenviron/gohlslib/v2/pkg/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	mp4codecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// aacFrameSamples is the constant sample count of one AAC-LC frame.
const aacFrameSamples = 1024

type sample struct {
	dts       int64
	ptsOffset int32
	isSync    bool
	payload   []byte
}

type trackState struct {
	track    *core.Track
	isVideo  bool
	h264Ext  *h264.DTSExtractor
	h265Ext  *h265.DTSExtractor
	pending  []sample
	firstDTS int64
	hasFirst bool
}

// Fragmenter converts core.Units into MSE-ready fMP4: an init segment plus one
// fragment per video GOP (keyframe-delimited). It is not safe for concurrent
// use; drive it from a single goroutine.
type Fragmenter struct {
	tracks []*core.Track
	states map[int]*trackState
	hasVid bool
	seq    uint32
}

// NewFragmenter prepares a fragmenter for the stream's tracks.
func NewFragmenter(tracks []*core.Track) *Fragmenter {
	f := &Fragmenter{states: make(map[int]*trackState, len(tracks))}
	for _, t := range tracks {
		ts := &trackState{track: t, isVideo: t.IsVideo()}
		switch t.Codec {
		case core.CodecH264:
			ts.h264Ext = h264.NewDTSExtractor()
		case core.CodecH265:
			ts.h265Ext = h265.NewDTSExtractor()
		}
		f.states[t.ID] = ts
		f.tracks = append(f.tracks, t)
		if ts.isVideo {
			f.hasVid = true
		}
	}
	return f
}

// InitSegment returns the fMP4 init segment (ftyp+moov) describing all tracks.
func (f *Fragmenter) InitSegment() ([]byte, error) {
	init := &fmp4.Init{}
	for _, t := range f.tracks {
		codec, err := mp4CodecFor(t)
		if err != nil {
			return nil, err
		}
		init.Tracks = append(init.Tracks, &fmp4.InitTrack{
			ID:        t.ID,
			TimeScale: uint32(t.ClockRate),
			Codec:     codec,
		})
	}
	var buf seekablebuffer.Buffer
	if err := init.Marshal(&buf); err != nil {
		return nil, fmt.Errorf("marshaling init segment: %w", err)
	}
	return buf.Bytes(), nil
}

// MimeCodec returns the MSE MIME type, e.g.
// `video/mp4; codecs="hvc1.1.6.L150.0, mp4a.40.2"`.
func (f *Fragmenter) MimeCodec() string {
	var parts []string
	for _, t := range f.tracks {
		if c := ghCodecFor(t); c != nil {
			if s := codecparams.Marshal(c); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return fmt.Sprintf(`video/mp4; codecs="%s"`, strings.Join(parts, ", "))
}

// Push feeds one unit. It returns a fragment (moof+mdat) when a video GOP has
// just completed, or nil otherwise. For audio-only streams a fragment is
// emitted roughly once per second.
func (f *Fragmenter) Push(u *core.Unit) ([]byte, error) {
	ts := f.states[u.TrackID]
	if ts == nil {
		return nil, nil
	}
	if ts.isVideo {
		return f.pushVideo(ts, u)
	}
	return f.pushAudio(ts, u)
}

func (f *Fragmenter) pushVideo(ts *trackState, u *core.Unit) ([]byte, error) {
	dts, err := extractDTS(ts, u)
	if err != nil {
		return nil, nil // decode order not established yet (waiting for IDR)
	}

	var frag []byte
	if u.RandomAccess && len(ts.pending) > 0 {
		// A new keyframe closes the previous GOP; emit it, using this keyframe's
		// DTS as the boundary that gives the GOP's last frame its duration.
		frag, err = f.flush(dts)
		if err != nil {
			return nil, err
		}
	}

	payload, err := h264.AVCC(u.AUs).Marshal() // length-prefixed; same for H.265
	if err != nil {
		return nil, err
	}
	ts.pending = append(ts.pending, sample{
		dts:       dts,
		ptsOffset: int32(u.PTS - dts),
		isSync:    u.RandomAccess,
		payload:   payload,
	})
	return frag, nil
}

func (f *Fragmenter) pushAudio(ts *trackState, u *core.Unit) ([]byte, error) {
	for i, frame := range u.AUs {
		ts.pending = append(ts.pending, sample{
			dts:     u.PTS + int64(i*aacFrameSamples),
			isSync:  true,
			payload: frame,
		})
	}
	// With no video to delimit GOPs, flush about once per second.
	if !f.hasVid && len(ts.pending) >= ts.track.ClockRate/aacFrameSamples {
		last := ts.pending[len(ts.pending)-1].dts
		return f.flush(last + aacFrameSamples)
	}
	return nil, nil
}

// flush emits one fragment containing every track's pending samples.
// videoBoundaryDTS supplies the duration of the last video sample.
func (f *Fragmenter) flush(videoBoundaryDTS int64) ([]byte, error) {
	var partTracks []*fmp4.PartTrack
	for _, t := range f.tracks {
		ts := f.states[t.ID]
		if len(ts.pending) == 0 {
			continue
		}
		if !ts.hasFirst {
			ts.firstDTS = ts.pending[0].dts
			ts.hasFirst = true
		}
		pt := &fmp4.PartTrack{
			ID:       t.ID,
			BaseTime: uint64(ts.pending[0].dts - ts.firstDTS),
		}
		for i, s := range ts.pending {
			var dur uint32
			switch {
			case i+1 < len(ts.pending):
				dur = uint32(ts.pending[i+1].dts - s.dts)
			case ts.isVideo:
				dur = uint32(videoBoundaryDTS - s.dts)
			default:
				dur = aacFrameSamples
			}
			pt.Samples = append(pt.Samples, &fmp4.Sample{
				Duration:        dur,
				PTSOffset:       s.ptsOffset,
				IsNonSyncSample: !s.isSync,
				Payload:         s.payload,
			})
		}
		partTracks = append(partTracks, pt)
		ts.pending = ts.pending[:0]
	}
	if len(partTracks) == 0 {
		return nil, nil
	}
	f.seq++
	part := &fmp4.Part{SequenceNumber: f.seq, Tracks: partTracks}
	var buf seekablebuffer.Buffer
	if err := part.Marshal(&buf); err != nil {
		return nil, fmt.Errorf("marshaling fragment: %w", err)
	}
	return buf.Bytes(), nil
}

func extractDTS(ts *trackState, u *core.Unit) (int64, error) {
	switch {
	case ts.h264Ext != nil:
		return ts.h264Ext.Extract(u.AUs, u.PTS)
	case ts.h265Ext != nil:
		return ts.h265Ext.Extract(u.AUs, u.PTS)
	default:
		return u.PTS, nil
	}
}

// mp4CodecFor maps a core.Track to a mediacommon MP4 codec (for the init box).
func mp4CodecFor(t *core.Track) (mp4codecs.Codec, error) {
	switch t.Codec {
	case core.CodecH264:
		return &mp4codecs.H264{SPS: t.SPS, PPS: t.PPS}, nil
	case core.CodecH265:
		return &mp4codecs.H265{VPS: t.VPS, SPS: t.SPS, PPS: t.PPS}, nil
	case core.CodecAAC:
		if t.AudioConfig == nil {
			return nil, fmt.Errorf("aac track %d has no config", t.ID)
		}
		return &mp4codecs.MPEG4Audio{Config: *t.AudioConfig}, nil
	default:
		return nil, fmt.Errorf("unsupported codec %q", t.Codec)
	}
}

// ghCodecFor maps a core.Track to a gohlslib codec (for the RFC 6381 string).
func ghCodecFor(t *core.Track) ghcodecs.Codec {
	switch t.Codec {
	case core.CodecH264:
		return &ghcodecs.H264{SPS: t.SPS, PPS: t.PPS}
	case core.CodecH265:
		return &ghcodecs.H265{VPS: t.VPS, SPS: t.SPS, PPS: t.PPS}
	case core.CodecAAC:
		if t.AudioConfig == nil {
			return nil
		}
		return &ghcodecs.MPEG4Audio{Config: *t.AudioConfig}
	default:
		return nil
	}
}
