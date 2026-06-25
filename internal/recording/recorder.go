package recording

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// aacFrameSamples is the number of PCM samples in one AAC-LC frame; AAC frame
// duration in clock-rate units is therefore constant.
const aacFrameSamples = 1024

// Sink receives recording lifecycle events (typically backed by the database).
type Sink interface {
	// SegmentStarted is called when a new file begins; it returns the
	// recording ID to use when finalizing.
	SegmentStarted(cameraID, relPath string, start time.Time) (string, error)
	// SegmentFinalized is called when a file is closed.
	SegmentFinalized(recordingID string, end time.Time, durationMS, sizeBytes int64) error
}

// Recorder consumes a stream and writes rotating fMP4 segment files.
type Recorder struct {
	CameraID   string
	CameraName string
	Root       string
	SegmentDur time.Duration
	Template   string
	// AlignToClock cuts segments on wall-clock boundaries (e.g. 10-minute
	// segments start at :00, :10, :20) instead of SegmentDur after the previous
	// cut. The cut still lands on the next keyframe, since stream copy cannot
	// split a GOP.
	AlignToClock bool
	Sink         Sink
	Log          *slog.Logger

	// per-track buffering state
	trackStates map[int]*trackBuf
	videoTrack  *core.Track

	// current file
	writer       *mp4Writer
	recordingID  string
	relPath      string
	segmentStart time.Time
	segmentEnd   time.Time // when the current segment should rotate
}

type sample struct {
	dts       int64
	ptsOffset int32
	isSync    bool
	payload   []byte
}

type trackBuf struct {
	track     *core.Track
	isVideo   bool
	h264Ext   *h264.DTSExtractor
	h265Ext   *h265.DTSExtractor
	pending   []sample
	fileStart int64
	hasStart  bool
	lastDTS   int64
	hasLast   bool
}

// Run records the stream until ctx is cancelled or the stream ends.
func (r *Recorder) Run(ctx context.Context, stream *core.Stream) error {
	if r.SegmentDur <= 0 {
		r.SegmentDur = 10 * time.Minute
	}
	r.initTracks(stream.Tracks())

	reader := stream.AddReader(2048)
	defer reader.Close()
	defer r.finalize()

	for {
		select {
		case <-ctx.Done():
			return nil
		case u, ok := <-reader.Units():
			if !ok {
				return nil
			}
			if err := r.handle(u); err != nil {
				if r.Log != nil {
					r.Log.Error("recorder error", "camera", r.CameraID, "err", err)
				}
				return err
			}
		}
	}
}

func (r *Recorder) initTracks(tracks []*core.Track) {
	r.trackStates = make(map[int]*trackBuf, len(tracks))
	for _, t := range tracks {
		tb := &trackBuf{track: t, isVideo: t.IsVideo()}
		switch t.Codec {
		case core.CodecH264:
			tb.h264Ext = h264.NewDTSExtractor()
		case core.CodecH265:
			tb.h265Ext = h265.NewDTSExtractor()
		}
		r.trackStates[t.ID] = tb
		if tb.isVideo && r.videoTrack == nil {
			r.videoTrack = t
		}
	}
}

func (r *Recorder) handle(u *core.Unit) error {
	tb := r.trackStates[u.TrackID]
	if tb == nil {
		return nil
	}
	if tb.isVideo {
		return r.handleVideo(tb, u)
	}
	return r.handleAudio(tb, u)
}

func (r *Recorder) handleVideo(tb *trackBuf, u *core.Unit) error {
	dts, err := extractDTS(tb, u)
	if err != nil {
		// Can't establish decode order yet (e.g. waiting for the first IDR).
		return nil
	}

	if u.RandomAccess {
		// Close out the previous GOP into the current file, then rotate if due.
		if len(tb.pending) > 0 {
			if err := r.flush(dts); err != nil {
				return err
			}
		}
		if r.writer == nil {
			if err := r.openSegment(u.NTP); err != nil {
				return err
			}
		} else if r.rotateDue(u.NTP) {
			if err := r.finalize(); err != nil {
				return err
			}
			if err := r.openSegment(u.NTP); err != nil {
				return err
			}
		}
	}

	payload, err := videoAVCC(tb.track.Codec, u.AUs)
	if err != nil {
		return err
	}
	tb.pending = append(tb.pending, sample{
		dts:       dts,
		ptsOffset: int32(u.PTS - dts),
		isSync:    u.RandomAccess,
		payload:   payload,
	})
	tb.lastDTS = dts
	tb.hasLast = true
	return nil
}

func (r *Recorder) handleAudio(tb *trackBuf, u *core.Unit) error {
	// Audio frames have a constant duration and no reordering.
	for i, frame := range u.AUs {
		dts := u.PTS + int64(i*aacFrameSamples)
		tb.pending = append(tb.pending, sample{
			dts:     dts,
			isSync:  true,
			payload: frame,
		})
		tb.lastDTS = dts
		tb.hasLast = true
	}

	// When there is no video track to drive flushing, flush audio on a ~1s
	// cadence and rotate by wall clock.
	if r.videoTrack == nil && len(tb.pending) >= tb.track.ClockRate/aacFrameSamples {
		if r.writer == nil {
			if err := r.openSegment(u.NTP); err != nil {
				return err
			}
		} else if r.rotateDue(u.NTP) {
			if err := r.finalize(); err != nil {
				return err
			}
			if err := r.openSegment(u.NTP); err != nil {
				return err
			}
		}
		return r.flush(tb.lastDTS + aacFrameSamples)
	}
	return nil
}

// flush writes one fragment containing all pending samples of every track.
// videoBoundaryDTS is the DTS of the sample that ends the current video GOP and
// supplies the duration of the GOP's last frame.
func (r *Recorder) flush(videoBoundaryDTS int64) error {
	if r.writer == nil {
		return nil
	}
	var partTracks []*fmp4.PartTrack
	for _, tb := range r.trackStates {
		if len(tb.pending) == 0 {
			continue
		}
		if !tb.hasStart {
			tb.fileStart = tb.pending[0].dts
			tb.hasStart = true
		}
		pt := &fmp4.PartTrack{
			ID:       tb.track.ID,
			BaseTime: uint64(tb.pending[0].dts - tb.fileStart),
		}
		for i, s := range tb.pending {
			var dur uint32
			if i+1 < len(tb.pending) {
				dur = uint32(tb.pending[i+1].dts - s.dts)
			} else if tb.isVideo {
				dur = uint32(videoBoundaryDTS - s.dts)
			} else {
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
		tb.pending = tb.pending[:0]
	}
	return r.writer.writeFragment(partTracks)
}

func (r *Recorder) openSegment(start time.Time) error {
	if start.IsZero() {
		start = time.Now()
	}
	rel := RenderPath(r.Template, r.CameraID, r.CameraName, start)
	abs := filepath.Join(r.Root, filepath.FromSlash(rel))

	w, err := newMP4Writer(abs, r.tracksSlice())
	if err != nil {
		return err
	}
	r.writer = w
	r.relPath = rel
	r.segmentStart = start
	if r.AlignToClock {
		r.segmentEnd = nextAlignedBoundary(start, r.SegmentDur)
	} else {
		r.segmentEnd = start.Add(r.SegmentDur)
	}

	// reset per-file timeline
	for _, tb := range r.trackStates {
		tb.hasStart = false
	}

	id, err := r.Sink.SegmentStarted(r.CameraID, rel, start)
	if err != nil {
		_ = w.close()
		r.writer = nil
		return fmt.Errorf("registering recording: %w", err)
	}
	r.recordingID = id
	if r.Log != nil {
		r.Log.Info("recording started", "camera", r.CameraID, "path", rel)
	}
	return nil
}

// rotateDue reports whether the current segment should rotate at time t.
func (r *Recorder) rotateDue(t time.Time) bool {
	if r.segmentEnd.IsZero() {
		return false
	}
	return !t.Before(r.segmentEnd)
}

// nextAlignedBoundary returns the next wall-clock segment boundary strictly
// after t, aligning to multiples of dur from local midnight — so e.g. 10-minute
// segments end at :00, :10, :20. A dur that does not divide the day evenly
// simply restarts the cadence at midnight (the last segment of the day is
// short), matching FFmpeg's -segment_atclocktime.
func nextAlignedBoundary(t time.Time, dur time.Duration) time.Time {
	if dur <= 0 {
		return t
	}
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	n := int64(t.Sub(midnight) / dur)
	return midnight.Add(time.Duration(n+1) * dur)
}

// finalize flushes any tail samples, closes the file and reports it.
func (r *Recorder) finalize() error {
	if r.writer == nil {
		return nil
	}

	// Flush trailing buffered samples, estimating the last video frame's
	// duration from the previous interval.
	r.flushTail()

	size := r.writer.size()
	if err := r.writer.close(); err != nil && r.Log != nil {
		r.Log.Error("closing recording", "err", err)
	}

	end := time.Now()
	durationMS := r.fileDurationMS()
	if r.Sink != nil && r.recordingID != "" {
		if err := r.Sink.SegmentFinalized(r.recordingID, end, durationMS, size); err != nil && r.Log != nil {
			r.Log.Error("finalizing recording", "err", err)
		}
	}
	if r.Log != nil {
		r.Log.Info("recording finalized", "camera", r.CameraID, "path", r.relPath, "bytes", size, "ms", durationMS)
	}

	r.writer = nil
	r.recordingID = ""
	r.relPath = ""
	return nil
}

func (r *Recorder) flushTail() {
	// Estimate a boundary DTS one frame beyond the last buffered video sample.
	var boundary int64
	if tb := r.videoBuf(); tb != nil && len(tb.pending) > 0 {
		last := tb.pending[len(tb.pending)-1]
		step := int64(tb.track.ClockRate) / 30 // assume ~30fps if unknown
		if len(tb.pending) >= 2 {
			step = last.dts - tb.pending[len(tb.pending)-2].dts
		}
		boundary = last.dts + step
	}
	_ = r.flush(boundary)
}

func (r *Recorder) fileDurationMS() int64 {
	tb := r.videoBuf()
	if tb == nil {
		// fall back to any track
		for _, t := range r.trackStates {
			tb = t
			break
		}
	}
	if tb == nil || !tb.hasLast || !tb.hasStart {
		return 0
	}
	return (tb.lastDTS - tb.fileStart) * 1000 / int64(tb.track.ClockRate)
}

func (r *Recorder) videoBuf() *trackBuf {
	if r.videoTrack == nil {
		return nil
	}
	return r.trackStates[r.videoTrack.ID]
}

func (r *Recorder) tracksSlice() []*core.Track {
	out := make([]*core.Track, 0, len(r.trackStates))
	// preserve deterministic order by track ID
	for id := 1; id <= len(r.trackStates)+8; id++ {
		if tb, ok := r.trackStates[id]; ok {
			out = append(out, tb.track)
		}
	}
	return out
}

func extractDTS(tb *trackBuf, u *core.Unit) (int64, error) {
	switch {
	case tb.h264Ext != nil:
		return tb.h264Ext.Extract(u.AUs, u.PTS)
	case tb.h265Ext != nil:
		return tb.h265Ext.Extract(u.AUs, u.PTS)
	default:
		return u.PTS, nil
	}
}

// videoAVCC length-prefixes the NAL units of an access unit. The 4-byte
// big-endian length format is identical for H.264 and H.265 MP4 samples.
func videoAVCC(codec core.Codec, aus [][]byte) ([]byte, error) {
	_ = codec
	return h264.AVCC(aus).Marshal()
}
