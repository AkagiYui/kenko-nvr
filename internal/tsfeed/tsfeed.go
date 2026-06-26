// Package tsfeed serializes a live core.Stream into an MPEG-TS byte stream. It
// is the shared building block for anything that needs the camera's media as a
// standard container: feeding FFmpeg's stdin (live transcode, motion detection)
// or serving HTTP-TS to a player.
package tsfeed

import (
	"context"
	"io"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts"
	mpegtscodecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts/codecs"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// tsClockRate is the 90 kHz clock used by MPEG-TS timestamps.
const tsClockRate = 90000

// Feed writes src to w as MPEG-TS until ctx is cancelled, the stream ends, or w
// errors. It attaches its own reader to src, so multiple consumers can feed
// independently. Returns io.EOF on a clean stream end.
func Feed(ctx context.Context, src *core.Stream, w io.Writer) error {
	byID, tsTracks := buildTracks(src.Tracks())
	if len(tsTracks) == 0 {
		return io.EOF
	}
	mw := mpegts.NewWriter(w, tsTracks)
	reader := src.AddReader(1024)
	defer reader.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case u, ok := <-reader.Units():
			if !ok {
				return io.EOF
			}
			if err := writeUnit(mw, byID[u.TrackID], u); err != nil {
				return err
			}
		}
	}
}

type track struct {
	core *core.Track
	mts  *mpegts.Track
	h264 *h264.DTSExtractor
	h265 *h265.DTSExtractor
}

func buildTracks(tracks []*core.Track) (map[int]*track, []*mpegts.Track) {
	byID := make(map[int]*track, len(tracks))
	var mtsTracks []*mpegts.Track
	for _, tr := range tracks {
		var codec mpegtscodecs.Codec
		t := &track{core: tr}
		switch tr.Codec {
		case core.CodecH264:
			codec = &mpegtscodecs.H264{}
			t.h264 = h264.NewDTSExtractor()
		case core.CodecH265:
			codec = &mpegtscodecs.H265{}
			t.h265 = h265.NewDTSExtractor()
		case core.CodecAAC:
			if tr.AudioConfig == nil {
				continue
			}
			codec = &mpegtscodecs.MPEG4Audio{Config: *tr.AudioConfig}
		default:
			continue
		}
		t.mts = &mpegts.Track{Codec: codec}
		byID[tr.ID] = t
		mtsTracks = append(mtsTracks, t.mts)
	}
	return byID, mtsTracks
}

func writeUnit(w *mpegts.Writer, t *track, u *core.Unit) error {
	if t == nil {
		return nil
	}
	pts := rescale(u.PTS, int64(t.core.ClockRate), tsClockRate)
	switch {
	case t.h264 != nil:
		dts, err := t.h264.Extract(u.AUs, u.PTS)
		if err != nil {
			return nil // decode order not established yet
		}
		return w.WriteH264(t.mts, pts, rescale(dts, int64(t.core.ClockRate), tsClockRate), u.AUs)
	case t.h265 != nil:
		dts, err := t.h265.Extract(u.AUs, u.PTS)
		if err != nil {
			return nil
		}
		return w.WriteH265(t.mts, pts, rescale(dts, int64(t.core.ClockRate), tsClockRate), u.AUs)
	default:
		return w.WriteMPEG4Audio(t.mts, pts, u.AUs)
	}
}

// rescale converts a timestamp between clock rates without overflowing int64.
func rescale(v, from, to int64) int64 {
	if from == to || from == 0 {
		return v
	}
	return v/from*to + (v%from)*to/from
}
