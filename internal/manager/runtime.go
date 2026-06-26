package manager

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/AkagiYui/kenko-nvr/internal/core"
	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/hls"
	"github.com/AkagiYui/kenko-nvr/internal/motion"
	"github.com/AkagiYui/kenko-nvr/internal/notify"
	"github.com/AkagiYui/kenko-nvr/internal/recording"
	"github.com/AkagiYui/kenko-nvr/internal/transcode"
)

// motionPostRoll keeps motion-mode recording active this long after motion ends
// (on top of the detector's own end debounce), so the tail of an event is kept.
const motionPostRoll = 5 * time.Second

// camRuntime owns the lifecycle of a single camera's media pipeline.
type camRuntime struct {
	mgr    *Manager
	camera database.Camera

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	state  core.SourceState
	errMsg string
	stream *core.Stream
	muxer  *hls.Muxer

	// liveTC is the on-demand transcoder for browser live view of a non-H.264
	// source; liveTCStream is the source stream it is bound to (it is rebuilt
	// when the source stream is replaced on reconnect).
	liveTC       *transcode.LiveTranscoder
	liveTCStream *core.Stream

	// pushCtx cancels consumers attached to an RTMP push.
	pushCancel context.CancelFunc

	// motion detection state, guarded by motionMu.
	motionMu      sync.Mutex
	motionActive  bool
	motionEndAt   time.Time
	motionEventID string
}

// motionGate reports whether motion-triggered recording should be active at t:
// true while motion is active or within the post-roll window after it ended.
func (rt *camRuntime) motionGate(t time.Time) bool {
	rt.motionMu.Lock()
	defer rt.motionMu.Unlock()
	if rt.motionActive {
		return true
	}
	return !rt.motionEndAt.IsZero() && t.Sub(rt.motionEndAt) < motionPostRoll
}

func (rt *camRuntime) motionIsActive() bool {
	rt.motionMu.Lock()
	defer rt.motionMu.Unlock()
	return rt.motionActive
}

// onMotionStart records the start of a motion event and fires a notification.
func (rt *camRuntime) onMotionStart(t time.Time) {
	id := uuid.NewString()
	rt.motionMu.Lock()
	rt.motionActive = true
	rt.motionEventID = id
	rt.motionMu.Unlock()

	_ = rt.mgr.db.Events.Create(database.Event{
		ID:        id,
		CameraID:  rt.camera.ID,
		Type:      database.EventMotion,
		StartTime: t,
	})
	if rt.mgr.log != nil {
		rt.mgr.log.Info("motion started", "camera", rt.camera.ID)
	}
	rt.mgr.notify(notify.Notification{
		Kind:       "motion",
		CameraID:   rt.camera.ID,
		CameraName: rt.camera.Name,
		Title:      "检测到移动 · " + rt.camera.Name,
		Body:       "摄像头「" + rt.camera.Name + "」检测到移动。",
		Time:       t,
	})
}

// onMotionEnd closes the current motion event.
func (rt *camRuntime) onMotionEnd(t time.Time, score float64) {
	rt.motionMu.Lock()
	rt.motionActive = false
	rt.motionEndAt = t
	id := rt.motionEventID
	rt.motionEventID = ""
	rt.motionMu.Unlock()

	if id != "" {
		_ = rt.mgr.db.Events.Finalize(id, t, score)
	}
	if rt.mgr.log != nil {
		rt.mgr.log.Debug("motion ended", "camera", rt.camera.ID, "score", score)
	}
}

func (rt *camRuntime) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	rt.cancel = cancel

	if rt.camera.SourceType == database.SourceRTMP {
		// Push cameras wait passively for an incoming publish; nothing to dial.
		rt.setState(core.StateConnecting, "")
		return
	}

	rt.mgr.wg.Add(1)
	rt.wg.Add(1)
	go func() {
		defer rt.mgr.wg.Done()
		defer rt.wg.Done()
		rt.supervise(ctx)
	}()
}

func (rt *camRuntime) stop() {
	if rt.cancel != nil {
		rt.cancel()
	}
	rt.mu.Lock()
	tc := rt.liveTC
	rt.liveTC = nil
	rt.liveTCStream = nil
	if rt.stream != nil {
		rt.stream.Close()
	}
	rt.mu.Unlock()
	if tc != nil {
		tc.Close()
	}
	rt.wg.Wait()
}

// liveStream returns a browser-playable (H.264) live stream and a release
// callback. For an H.264 source it hands back the original stream directly; for
// a non-H.264 source it acquires a viewer on the shared on-demand transcoder,
// falling back to the original stream if no encoder is available.
func (rt *camRuntime) liveStream(ctx context.Context) (*core.Stream, func(), bool) {
	stream := rt.currentStream()
	if stream == nil {
		return nil, nil, false
	}

	// Direct fan-out when the source is already browser-friendly.
	if v := stream.VideoTrack(); v == nil || v.Codec == core.CodecH264 {
		return stream, func() {}, true
	}

	enc := rt.mgr.liveEncoder
	if enc == nil {
		// No FFmpeg/encoder: serve the source unchanged (better than a 503; the
		// browser may still play it, e.g. H.265 in Safari).
		return stream, func() {}, true
	}

	rt.mu.Lock()
	if rt.liveTC == nil || rt.liveTCStream != stream {
		if rt.liveTC != nil {
			rt.liveTC.Close()
		}
		rt.liveTC = &transcode.LiveTranscoder{
			Source:  stream,
			Encoder: enc,
			Bitrate: rt.mgr.cfg.Transcode.LiveBitrateKbps,
			GOP:     rt.mgr.cfg.Transcode.LiveGOP,
			Log:     rt.mgr.log,
		}
		rt.liveTCStream = stream
	}
	tc := rt.liveTC
	rt.mu.Unlock()

	out, err := tc.Acquire(ctx)
	if err != nil {
		if rt.mgr.log != nil {
			rt.mgr.log.Warn("live transcode unavailable; serving source stream",
				"camera", rt.camera.ID, "err", err)
		}
		return stream, func() {}, true
	}
	return out, tc.Release, true
}

// supervise runs the pull source in a reconnect loop.
func (rt *camRuntime) supervise(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			rt.setState(core.StateIdle, "")
			return
		}

		src := rt.mgr.buildPullSource(rt.camera)
		if src == nil {
			rt.setState(core.StateError, "unsupported source type")
			return
		}

		rt.setState(core.StateConnecting, "")
		runCtx, cancel := context.WithCancel(ctx)
		readyCh := make(chan *core.Stream, 1)
		errCh := make(chan error, 1)

		go func() {
			errCh <- src.Run(runCtx, func(s *core.Stream) {
				select {
				case readyCh <- s:
				default:
				}
			})
		}()

		select {
		case <-ctx.Done():
			cancel()
			<-errCh
			rt.setState(core.StateIdle, "")
			return

		case stream := <-readyCh:
			rt.setStream(stream)
			rt.setState(core.StateRunning, "")
			rt.startConsumers(runCtx, stream)
			backoff = time.Second // healthy connection resets backoff

			err := <-errCh // blocks until the source ends
			stream.Close()
			rt.clearStream()
			cancel()
			if ctx.Err() == nil {
				rt.setState(core.StateError, errString(err))
				// The camera was live and just dropped: alert (throttled by the
				// notifier and gated by the OnCameraOffline setting).
				rt.mgr.notify(notify.Notification{
					Kind:       "offline",
					CameraID:   rt.camera.ID,
					CameraName: rt.camera.Name,
					Title:      "摄像头离线 · " + rt.camera.Name,
					Body:       "摄像头「" + rt.camera.Name + "」连接已断开。",
					Time:       time.Now(),
				})
			}

		case err := <-errCh:
			cancel()
			rt.setState(core.StateError, errString(err))
		}

		// backoff before reconnecting
		select {
		case <-ctx.Done():
			rt.setState(core.StateIdle, "")
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// startConsumers attaches the HLS muxer and (if enabled) the recorder to a
// live stream. They stop when the stream closes or ctx is cancelled.
func (rt *camRuntime) startConsumers(ctx context.Context, stream *core.Stream) {
	if mux, err := hls.New(stream, rt.mgr.log); err == nil {
		rt.setMuxer(mux)
		rt.wg.Add(1)
		go func() {
			defer rt.wg.Done()
			_ = mux.Run(ctx)
			rt.setMuxer(nil)
		}()
	} else {
		rt.mgr.log.Warn("hls unavailable", "camera", rt.camera.ID, "err", err)
	}

	// Motion detection: run when explicitly enabled or required by motion-mode
	// recording, provided FFmpeg is available and there is a video track.
	motionMode := rt.camera.RecordMode == "motion"
	if (rt.camera.MotionEnabled || motionMode) && motion.Available() && stream.VideoTrack() != nil {
		det := &motion.Detector{
			Source:      stream,
			Sensitivity: rt.camera.MotionSensitivity,
			Log:         rt.mgr.log,
			OnStart:     rt.onMotionStart,
			OnEnd:       rt.onMotionEnd,
		}
		rt.wg.Add(1)
		go func() {
			defer rt.wg.Done()
			_ = det.Run(ctx)
		}()
	} else if motionMode && !motion.Available() && rt.mgr.log != nil {
		rt.mgr.log.Warn("motion record mode requested but ffmpeg not found; recording continuously",
			"camera", rt.camera.ID)
	}

	if rt.camera.Record {
		rc := rt.mgr.recordingConfig()
		segDur := time.Duration(rc.SegmentSeconds) * time.Second

		// Motion-mode recording requires the gating stream-copy recorder, so it
		// overrides transcode recording.
		useGate := motionMode && motion.Available() && stream.VideoTrack() != nil

		var rec interface {
			Run(context.Context, *core.Stream) error
		}
		if rc.Transcode && !useGate && recording.TranscodeAvailable() {
			rec = &recording.TranscodeRecorder{
				CameraID:   rt.camera.ID,
				CameraName: rt.camera.Name,
				Root:       rt.mgr.recordingsRoot,
				SegmentDur: segDur,
				Template:   rc.PathTemplate,
				VideoCodec: rc.TranscodeVideoCodec,
				CRF:        rc.TranscodeCRF,
				Preset:     rc.TranscodePreset,
				Sink:       rt.mgr,
				Log:        rt.mgr.log,
			}
		} else {
			if rc.Transcode && !useGate && rt.mgr.log != nil {
				rt.mgr.log.Warn("transcode requested but ffmpeg not found; recording with stream copy",
					"camera", rt.camera.ID)
			}
			cr := &recording.Recorder{
				CameraID:     rt.camera.ID,
				CameraName:   rt.camera.Name,
				Root:         rt.mgr.recordingsRoot,
				SegmentDur:   segDur,
				Template:     rc.PathTemplate,
				AlignToClock: rc.AlignToClock,
				Sink:         rt.mgr,
				Log:          rt.mgr.log,
			}
			if useGate {
				cr.Gate = rt.motionGate
			}
			rec = cr
		}
		rt.wg.Add(1)
		go func() {
			defer rt.wg.Done()
			_ = rec.Run(ctx, stream)
		}()
	}
}

// attachPush wires consumers to a stream delivered by the RTMP server.
func (rt *camRuntime) attachPush(stream *core.Stream) {
	rt.detachPush()
	ctx, cancel := context.WithCancel(rt.mgr.ctx)

	rt.mu.Lock()
	rt.pushCancel = cancel
	rt.mu.Unlock()

	rt.setStream(stream)
	rt.setState(core.StateRunning, "")
	rt.startConsumers(ctx, stream)
}

func (rt *camRuntime) detachPush() {
	rt.mu.Lock()
	cancel := rt.pushCancel
	rt.pushCancel = nil
	stream := rt.stream
	rt.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if stream != nil {
		stream.Close()
	}
	rt.clearStream()
	rt.setState(core.StateConnecting, "")
}

// --- state accessors ----------------------------------------------------------

func (rt *camRuntime) setState(s core.SourceState, msg string) {
	rt.mu.Lock()
	rt.state = s
	rt.errMsg = msg
	rt.mu.Unlock()
}

func (rt *camRuntime) setStream(s *core.Stream) {
	rt.mu.Lock()
	rt.stream = s
	rt.mu.Unlock()
}

func (rt *camRuntime) clearStream() {
	rt.mu.Lock()
	rt.stream = nil
	tc := rt.liveTC
	rt.liveTC = nil
	rt.liveTCStream = nil
	rt.mu.Unlock()
	if tc != nil {
		tc.Close()
	}
}

func (rt *camRuntime) setMuxer(mux *hls.Muxer) {
	rt.mu.Lock()
	rt.muxer = mux
	rt.mu.Unlock()
}

func (rt *camRuntime) hlsMuxer() *hls.Muxer {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.muxer
}

func (rt *camRuntime) currentStream() *core.Stream {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.stream
}

func (rt *camRuntime) status() CameraStatus {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	st := CameraStatus{
		ID:        rt.camera.ID,
		State:     string(rt.state),
		Error:     rt.errMsg,
		Live:      rt.stream != nil,
		Recording: rt.camera.Record && rt.stream != nil,
		Motion:    rt.motionIsActive(),
	}
	if rt.stream != nil {
		for _, t := range rt.stream.Tracks() {
			kind := "video"
			if t.Kind == core.MediaAudio {
				kind = "audio"
			}
			st.Tracks = append(st.Tracks, TrackInfo{Kind: kind, Codec: string(t.Codec)})
		}
	}
	return st
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
