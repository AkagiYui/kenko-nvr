// Package hls converts a live core.Stream into HLS for browser playback.
package hls

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bluenviron/gohlslib/v2"
	"github.com/bluenviron/gohlslib/v2/pkg/codecs"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// Muxer wraps a gohlslib.Muxer and feeds it units from a core.Stream.
type Muxer struct {
	stream *core.Stream
	muxer  *gohlslib.Muxer
	log    *slog.Logger

	// mapping from core track ID to the gohlslib track
	tracks map[int]*gohlslib.Track
}

// New builds an HLS muxer for the stream. Only video and AAC audio tracks are
// muxed; unsupported tracks are skipped.
func New(stream *core.Stream, log *slog.Logger) (*Muxer, error) {
	m := &Muxer{
		stream: stream,
		log:    log,
		tracks: make(map[int]*gohlslib.Track),
	}

	var htracks []*gohlslib.Track
	for _, t := range stream.Tracks() {
		var ht *gohlslib.Track
		switch t.Codec {
		case core.CodecH264:
			ht = &gohlslib.Track{Codec: &codecs.H264{SPS: t.SPS, PPS: t.PPS}, ClockRate: t.ClockRate}
		case core.CodecH265:
			ht = &gohlslib.Track{Codec: &codecs.H265{VPS: t.VPS, SPS: t.SPS, PPS: t.PPS}, ClockRate: t.ClockRate}
		case core.CodecAAC:
			if t.AudioConfig == nil {
				continue
			}
			ht = &gohlslib.Track{Codec: &codecs.MPEG4Audio{Config: *t.AudioConfig}, ClockRate: t.ClockRate}
		default:
			continue
		}
		m.tracks[t.ID] = ht
		htracks = append(htracks, ht)
	}

	if len(htracks) == 0 {
		return nil, fmt.Errorf("no HLS-compatible tracks")
	}

	m.muxer = &gohlslib.Muxer{
		Tracks:             htracks,
		Variant:            gohlslib.MuxerVariantLowLatency,
		SegmentCount:       7,
		SegmentMinDuration: 1 * time.Second,
	}
	return m, nil
}

// Run starts the muxer and pumps units until ctx is cancelled or the stream
// ends.
func (m *Muxer) Run(ctx context.Context) error {
	if err := m.muxer.Start(); err != nil {
		return fmt.Errorf("starting hls muxer: %w", err)
	}
	defer m.muxer.Close()

	reader := m.stream.AddReader(1024)
	defer reader.Close()

	for {
		select {
		case <-ctx.Done():
			return nil
		case u, ok := <-reader.Units():
			if !ok {
				return nil
			}
			m.write(u)
		}
	}
}

func (m *Muxer) write(u *core.Unit) {
	ht := m.tracks[u.TrackID]
	if ht == nil {
		return
	}
	var err error
	switch c := ht.Codec.(type) {
	case *codecs.H264:
		_ = c
		err = m.muxer.WriteH264(ht, u.NTP, u.PTS, u.AUs)
	case *codecs.H265:
		err = m.muxer.WriteH265(ht, u.NTP, u.PTS, u.AUs)
	case *codecs.MPEG4Audio:
		err = m.muxer.WriteMPEG4Audio(ht, u.NTP, u.PTS, u.AUs)
	}
	if err != nil && m.log != nil {
		m.log.Debug("hls write error", "err", err)
	}
}

// ServeHTTP serves the HLS playlist and segments.
func (m *Muxer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.muxer.Handle(w, r)
}
