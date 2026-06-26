package gb28181

import (
	"log/slog"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// mediaProducer turns a sequence of reassembled PS frames into a core.Stream.
//
// It buffers frames until the video codec and its parameter sets (SPS/PPS, plus
// VPS for H.265) are known, then creates the stream, signals readiness once, and
// publishes one access unit per frame. Audio (typically G.711 in GB28181) is not
// modelled by core and is dropped; the stream is video-only.
type mediaProducer struct {
	log     *slog.Logger
	onReady func(*core.Stream)

	stream *core.Stream
	ready  bool
	codec  core.Codec
	vps    []byte
	sps    []byte
	pps    []byte

	havePrev bool
	prevTS   uint32
	pts      int64
	frames   int
}

func (p *mediaProducer) handleFrame(f rtpFrame) {
	res := demuxPS(f.data)
	if len(res.video) == 0 {
		return
	}
	nals := splitAnnexB(res.video)
	if len(nals) == 0 {
		return
	}
	p.frames++

	if p.codec == "" {
		p.codec = sniffCodec(res, nals)
	}
	p.collectParamSets(nals)

	// Advance the presentation clock (RTP video timestamp, 90 kHz), unwrapping
	// the 32-bit wrap with a signed delta.
	if p.havePrev {
		p.pts += int64(int32(f.timestamp - p.prevTS))
	}
	p.prevTS = f.timestamp
	p.havePrev = true

	if !p.ready {
		if p.tryReady() {
			p.ready = true
			if p.onReady != nil {
				p.onReady(p.stream)
			}
			if p.log != nil {
				p.log.Info("gb28181 stream ready", "codec", string(p.codec))
			}
		} else {
			return
		}
	}

	au := filterAU(nals)
	if len(au) == 0 {
		return
	}
	var randomAccess bool
	switch p.codec {
	case core.CodecH264:
		randomAccess = h264.IsRandomAccess(au)
	case core.CodecH265:
		randomAccess = h265.IsRandomAccess(au)
	}
	p.stream.WriteUnit(&core.Unit{
		TrackID:      1,
		PTS:          p.pts,
		NTP:          time.Now(),
		RandomAccess: randomAccess,
		AUs:          au,
	})
}

// tryReady builds the stream once the codec and required parameter sets are known.
func (p *mediaProducer) tryReady() bool {
	switch p.codec {
	case core.CodecH264:
		if p.sps == nil || p.pps == nil {
			return false
		}
		p.stream = core.NewStream([]*core.Track{{
			ID:        1,
			Kind:      core.MediaVideo,
			Codec:     core.CodecH264,
			ClockRate: 90000,
			SPS:       p.sps,
			PPS:       p.pps,
		}})
		return true
	case core.CodecH265:
		if p.sps == nil || p.pps == nil {
			return false
		}
		p.stream = core.NewStream([]*core.Track{{
			ID:        1,
			Kind:      core.MediaVideo,
			Codec:     core.CodecH265,
			ClockRate: 90000,
			VPS:       p.vps,
			SPS:       p.sps,
			PPS:       p.pps,
		}})
		return true
	default:
		return false
	}
}

// collectParamSets caches the latest SPS/PPS/VPS seen in the frame.
func (p *mediaProducer) collectParamSets(nals [][]byte) {
	for _, n := range nals {
		if len(n) == 0 {
			continue
		}
		switch p.codec {
		case core.CodecH264:
			switch n[0] & 0x1F {
			case 7:
				p.sps = cloneBytes(n)
			case 8:
				p.pps = cloneBytes(n)
			}
		case core.CodecH265:
			switch (n[0] >> 1) & 0x3F {
			case 32:
				p.vps = cloneBytes(n)
			case 33:
				p.sps = cloneBytes(n)
			case 34:
				p.pps = cloneBytes(n)
			}
		}
	}
}

// filterAU drops access-unit delimiters and filler NALs that would confuse the
// downstream fMP4 / HLS muxers; parameter sets are kept (harmless and useful on
// keyframes).
func filterAU(nals [][]byte) [][]byte {
	out := make([][]byte, 0, len(nals))
	for _, n := range nals {
		if len(n) == 0 {
			continue
		}
		out = append(out, n)
	}
	return out
}

// sniffCodec determines the video codec from the program_stream_map when present,
// otherwise from unambiguous NAL signatures, defaulting to H.264.
func sniffCodec(res psResult, nals [][]byte) core.Codec {
	switch res.videoCodec {
	case "H264":
		return core.CodecH264
	case "H265":
		return core.CodecH265
	}
	// H.265 VPS has first byte 0x40 (type 32), which is an unspecified/never-used
	// type in H.264 — an unambiguous H.265 signal.
	for _, n := range nals {
		if len(n) >= 2 && n[0] == 0x40 {
			return core.CodecH265
		}
	}
	// Otherwise commit to H.264 only once we have actually seen an SPS.
	for _, n := range nals {
		if len(n) >= 1 && n[0]&0x80 == 0 && n[0]&0x1F == 7 {
			return core.CodecH264
		}
	}
	return ""
}

// splitAnnexB splits an Annex-B elementary stream into NAL units (start codes
// removed). It accepts both 3- and 4-byte start codes.
func splitAnnexB(es []byte) [][]byte {
	var out [][]byte
	i := 0
	n := len(es)
	// Position i at the first start code.
	start := findStartCode(es, 0)
	if start < 0 {
		return nil
	}
	i = start
	for i < n {
		// Skip the start-code prefix.
		scLen := 3
		if i+3 < n && es[i] == 0 && es[i+1] == 0 && es[i+2] == 0 && es[i+3] == 1 {
			scLen = 4
		}
		nalStart := i + scLen
		next := findStartCode(es, nalStart)
		if next < 0 {
			if nalStart < n {
				out = append(out, es[nalStart:n])
			}
			break
		}
		if next > nalStart {
			out = append(out, es[nalStart:next])
		}
		i = next
	}
	return out
}

// findStartCode returns the index of the next 00 00 01 (possibly preceded by an
// extra 00) at or after from, or -1.
func findStartCode(es []byte, from int) int {
	for i := from; i+3 <= len(es); i++ {
		if es[i] == 0 && es[i+1] == 0 && es[i+2] == 1 {
			// Include a leading zero of a 4-byte start code.
			if i > from && es[i-1] == 0 {
				return i - 1
			}
			return i
		}
	}
	return -1
}

func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
