package recording

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	mp4codecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// mp4Writer writes a single fragmented-MP4 file: an init segment (ftyp+moov)
// followed by a sequence of fragments (moof+mdat).
type mp4Writer struct {
	path     string
	f        *os.File
	tracks   []*core.Track
	seq      uint32
	initDone bool
	bytes    int64
}

func newMP4Writer(path string, tracks []*core.Track) (*mp4Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating recording dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating recording file: %w", err)
	}
	return &mp4Writer{path: path, f: f, tracks: tracks}, nil
}

// writeInit emits the init segment describing all tracks.
func (w *mp4Writer) writeInit() error {
	init := &fmp4.Init{}
	for _, t := range w.tracks {
		codec, err := mp4CodecFor(t)
		if err != nil {
			return err
		}
		init.Tracks = append(init.Tracks, &fmp4.InitTrack{
			ID:        t.ID,
			TimeScale: uint32(t.ClockRate),
			Codec:     codec,
		})
	}
	if err := init.Marshal(w.f); err != nil {
		return fmt.Errorf("marshaling init segment: %w", err)
	}
	if err := w.syncEnd(); err != nil {
		return err
	}
	w.initDone = true
	return nil
}

// writeFragment appends one moof+mdat fragment.
func (w *mp4Writer) writeFragment(partTracks []*fmp4.PartTrack) error {
	if len(partTracks) == 0 {
		return nil
	}
	if !w.initDone {
		if err := w.writeInit(); err != nil {
			return err
		}
	}
	w.seq++
	part := &fmp4.Part{
		SequenceNumber: w.seq,
		Tracks:         partTracks,
	}
	if err := part.Marshal(w.f); err != nil {
		return fmt.Errorf("marshaling fragment: %w", err)
	}
	return w.syncEnd()
}

// syncEnd repositions the file cursor at EOF (Marshal may seek to patch box
// sizes) and refreshes the running byte count.
func (w *mp4Writer) syncEnd() error {
	end, err := w.f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	w.bytes = end
	return nil
}

func (w *mp4Writer) size() int64 { return w.bytes }

func (w *mp4Writer) close() error {
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// mp4CodecFor maps a core.Track to a mediacommon MP4 codec descriptor.
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
		return nil, fmt.Errorf("unsupported codec %q for mp4", t.Codec)
	}
}
