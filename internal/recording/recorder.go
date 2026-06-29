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
	// Gate, when non-nil, decides at each keyframe whether recording should be
	// active right now. It powers event-triggered (motion) recording: while it
	// returns false no segment is written, and an open segment is closed. When
	// nil the recorder always records (continuous mode).
	Gate func(t time.Time) bool
	// PreRoll, when > 0 alongside a Gate, makes the recorder keep a rolling
	// buffer of the most recent GOPs while gated off; when the gate opens those
	// GOPs are written first, so the segment includes this much footage from
	// before the trigger (motion pre-roll). Ignored in continuous mode.
	PreRoll time.Duration
	// Clock returns the current wall-clock time; nil means time.Now. Injectable
	// so tests can drive segment timing deterministically.
	Clock func() time.Time
	Sink  Sink
	Log   *slog.Logger

	// per-track buffering state
	trackStates map[int]*trackBuf
	videoTrack  *core.Track

	// Pre-roll GOP ring buffer (see PreRoll) and the wall-clock time of the
	// keyframe that opened the GOP currently accumulating in the track buffers.
	gopCache []cachedGOP
	gopWall  time.Time

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

// cachedGOP is one completed group-of-pictures held for motion pre-roll: a
// snapshot of every track's samples, the DTS that bounds the last video frame's
// duration, and the wall-clock time of the GOP's keyframe.
type cachedGOP struct {
	tracks      map[int][]sample
	boundaryDTS int64
	wall        time.Time
}

// now returns the current wall-clock time (overridable via Clock for tests).
func (r *Recorder) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

// preRollEnabled reports whether pre-roll GOP buffering is active.
func (r *Recorder) preRollEnabled() bool { return r.PreRoll > 0 && r.Gate != nil }

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
		// Anchor segment start/rotation to the server wall clock, sampled here.
		// finalize() takes end_time from the same clock, so start<=end always and
		// durations stay sane; the motion gate gets a time base consistent with
		// motionEndAt. (Units already carry server time in u.NTP, but sampling now
		// at the rotation point keeps clock-aligned cuts tracking the real wall
		// clock even if the reader is briefly behind.) Camera-supplied time is
		// never trusted anywhere — see core.Unit.NTP.
		now := r.now()
		active := r.Gate == nil || r.Gate(now)

		// Close out the GOP that just ended (its samples sit in pending across all
		// tracks): write it into the open file, stash it for pre-roll while gated
		// off, or drop it so buffers never grow unbounded.
		if len(tb.pending) > 0 {
			switch {
			case r.writer != nil:
				if err := r.flush(dts); err != nil {
					return err
				}
			case r.preRollEnabled():
				r.stashGOP(dts)
			default:
				r.clearPending()
			}
		}
		// This keyframe opens the next GOP; remember when, so it can be
		// timestamped if it is later stashed for pre-roll.
		r.gopWall = now

		switch {
		case active && r.writer == nil:
			// Start a fresh segment. With buffered pre-roll, begin the file at the
			// oldest buffered GOP and replay them first, so the recording includes
			// the footage from just before the trigger.
			start := now
			if g := r.oldestGOP(); g != nil {
				start = g.wall
			}
			if err := r.openSegment(start); err != nil {
				return err
			}
			if err := r.writePreroll(); err != nil {
				return err
			}
		case active && r.rotateDue(now):
			if err := r.finalize(); err != nil {
				return err
			}
			if err := r.openSegment(now); err != nil {
				return err
			}
		case !active && r.writer != nil:
			// Motion ended: close the current segment and wait for the next event.
			if err := r.finalize(); err != nil {
				return err
			}
		}

		// Gated off without pre-roll: nothing to write into, so drop the keyframe.
		// With pre-roll we fall through and buffer it instead.
		if !active && !r.preRollEnabled() {
			return nil
		}
	} else if r.writer == nil && !(r.preRollEnabled() && len(tb.pending) > 0) {
		// Before the first keyframe, or gated off without pre-roll buffering:
		// don't buffer inter frames.
		return nil
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
		now := r.now() // server wall clock; see handleVideo
		if r.writer == nil {
			if err := r.openSegment(now); err != nil {
				return err
			}
		} else if r.rotateDue(now) {
			if err := r.finalize(); err != nil {
				return err
			}
			if err := r.openSegment(now); err != nil {
				return err
			}
		}
		return r.flush(tb.lastDTS + aacFrameSamples)
	}
	return nil
}

// flush writes one fragment containing all pending samples of every track and
// clears the buffers. videoBoundaryDTS is the DTS of the sample that ends the
// current video GOP and supplies the duration of the GOP's last frame.
func (r *Recorder) flush(videoBoundaryDTS int64) error {
	if r.writer == nil {
		return nil
	}
	perTrack := make(map[int][]sample, len(r.trackStates))
	for id, tb := range r.trackStates {
		if len(tb.pending) > 0 {
			perTrack[id] = tb.pending
		}
	}
	if err := r.writeFrag(perTrack, videoBoundaryDTS); err != nil {
		return err
	}
	for _, tb := range r.trackStates {
		tb.pending = tb.pending[:0]
	}
	return nil
}

// writeFrag writes one fragment from the given per-track samples. It is the
// shared core of flush (live samples) and writePreroll (buffered GOPs), and
// advances each track's per-file timeline anchor (fileStart) on first use.
func (r *Recorder) writeFrag(perTrack map[int][]sample, videoBoundaryDTS int64) error {
	if r.writer == nil {
		return nil
	}
	var partTracks []*fmp4.PartTrack
	for id, samples := range perTrack {
		if len(samples) == 0 {
			continue
		}
		tb := r.trackStates[id]
		if !tb.hasStart {
			tb.fileStart = samples[0].dts
			tb.hasStart = true
		}
		pt := &fmp4.PartTrack{
			ID:       tb.track.ID,
			BaseTime: uint64(samples[0].dts - tb.fileStart),
		}
		for i, s := range samples {
			var dur uint32
			if i+1 < len(samples) {
				dur = uint32(samples[i+1].dts - s.dts)
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
	}
	return r.writer.writeFragment(partTracks)
}

// clearPending drops buffered samples on every track without writing them, used
// when recording is gated off (motion mode) so memory stays bounded.
func (r *Recorder) clearPending() {
	for _, tb := range r.trackStates {
		tb.pending = tb.pending[:0]
	}
}

// stashGOP snapshots the just-completed GOP (every track's pending samples) into
// the pre-roll ring buffer, then prunes it to the PreRoll window. boundaryDTS is
// the next keyframe's DTS, fixing the last video frame's duration.
func (r *Recorder) stashGOP(boundaryDTS int64) {
	g := cachedGOP{boundaryDTS: boundaryDTS, wall: r.gopWall, tracks: make(map[int][]sample, len(r.trackStates))}
	for id, tb := range r.trackStates {
		if len(tb.pending) == 0 {
			continue
		}
		cp := make([]sample, len(tb.pending))
		copy(cp, tb.pending)
		if !tb.isVideo {
			// Audio payloads alias the source unit's buffers; copy the bytes since
			// the GOP may be retained for several seconds. (Video payloads are
			// freshly marshaled by videoAVCC and are safe to retain by reference.)
			for i := range cp {
				b := make([]byte, len(cp[i].payload))
				copy(b, cp[i].payload)
				cp[i].payload = b
			}
		}
		g.tracks[id] = cp
		tb.pending = tb.pending[:0]
	}
	r.gopCache = append(r.gopCache, g)

	// Drop the oldest GOPs while the second-oldest still covers the window start,
	// keeping just enough to reach back ~PreRoll before the newest keyframe.
	newest := r.gopWall
	for len(r.gopCache) >= 2 && newest.Sub(r.gopCache[1].wall) >= r.PreRoll {
		r.gopCache = r.gopCache[1:]
	}
}

// oldestGOP returns the oldest buffered pre-roll GOP, or nil if none.
func (r *Recorder) oldestGOP() *cachedGOP {
	if len(r.gopCache) == 0 {
		return nil
	}
	return &r.gopCache[0]
}

// writePreroll writes every buffered pre-roll GOP into the freshly opened
// segment (oldest first) and empties the buffer.
func (r *Recorder) writePreroll() error {
	for i := range r.gopCache {
		g := &r.gopCache[i]
		if err := r.writeFrag(g.tracks, g.boundaryDTS); err != nil {
			return err
		}
	}
	r.gopCache = nil
	return nil
}

func (r *Recorder) openSegment(start time.Time) error {
	if start.IsZero() {
		start = r.now()
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

	end := r.now()
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
