// Package rechls serves recordings as on-demand H.264 HLS for browser-friendly,
// resumable, seekable playback.
//
// A recording is stored in its original codec (often H.265, which many browsers
// can't play in <video>, and whose fragmented-MP4 layout makes a bare element
// buffer the whole file). This package runs one ffmpeg per playback session that
// transcodes the file to H.264 and writes a growing HLS playlist + MPEG-TS
// segments into a temp dir; hls.js then loads only the segments around the
// playhead and retries them on a network blip — true resumable playback.
//
// Sessions are keyed by (recording, start-offset), reused across requests,
// reference only the disk cache, and are reaped after an idle period (and at
// shutdown). The H.264 encoder is the same hwaccel recipe the live transcoder
// uses, so hardware acceleration is reused where available.
package rechls

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/hwaccel"
)

const (
	segmentSeconds = 4
	defaultBitrate = 2500
	defaultGOP     = 96 // ~4s at 24fps, aligned to the segment length
	maxSessions    = 8
	readyTimeout   = 15 * time.Second
	defaultIdle    = 60 * time.Second
	gcInterval     = 20 * time.Second
)

// EncoderFunc returns the H.264 encoder recipe to use (nil -> software libx264).
type EncoderFunc func() *hwaccel.Encoder

// Manager owns the HLS transcode sessions and their disk cache.
type Manager struct {
	Root        string // base dir for session caches
	EncoderFn   EncoderFunc
	IdleTimeout time.Duration
	Log         *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	sid        string
	dir        string
	cancel     context.CancelFunc
	ready      chan struct{} // closed once index.m3u8 has a first segment (or failed)
	err        error
	lastAccess time.Time
}

// New creates a Manager, clearing any stale cache under root.
func New(root string, enc EncoderFunc, log *slog.Logger) *Manager {
	_ = os.RemoveAll(root)
	_ = os.MkdirAll(root, 0o755)
	return &Manager{
		Root:        root,
		EncoderFn:   enc,
		IdleTimeout: defaultIdle,
		Log:         log,
		sessions:    make(map[string]*session),
	}
}

// SessionID is the stable id for a (recording, offset) playback.
func SessionID(recordingID string, fromSec int) string {
	return fmt.Sprintf("%s_%d", recordingID, fromSec)
}

// Ensure starts (or reuses) the session for a recording file at fromSec and
// blocks until its playlist is playable. It returns the session id whose files
// are then served via Playlist/Segment.
func (m *Manager) Ensure(ctx context.Context, recordingID, filePath string, fromSec int) (string, error) {
	sid := SessionID(recordingID, fromSec)

	m.mu.Lock()
	if s, ok := m.sessions[sid]; ok {
		s.lastAccess = time.Now()
		m.mu.Unlock()
		<-s.ready
		return sid, s.err
	}
	m.evictLocked()
	s := &session{sid: sid, dir: filepath.Join(m.Root, sid), ready: make(chan struct{}), lastAccess: time.Now()}
	m.sessions[sid] = s
	m.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		s.err = err
		close(s.ready)
		m.drop(sid)
		return sid, err
	}

	// The ffmpeg lifetime is independent of the request that started it (later
	// requests reuse it); it is bound to a context cancelled by GC/Close.
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	args := m.ffmpegArgs(filePath, fromSec, s.dir)
	cmd := exec.CommandContext(runCtx, "ffmpeg", args...) //nolint:gosec // args from config + internal paths
	if err := cmd.Start(); err != nil {
		s.err = err
		close(s.ready)
		cancel()
		m.drop(sid)
		return sid, err
	}
	go func() {
		_ = cmd.Wait() // ffmpeg exits when the file is fully transcoded (ENDLIST) or cancelled
	}()

	go m.awaitReady(s)
	<-s.ready
	return sid, s.err
}

// awaitReady polls for the playlist's first segment so playback can begin before
// the whole file is transcoded.
func (m *Manager) awaitReady(s *session) {
	playlist := filepath.Join(s.dir, "index.m3u8")
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(playlist); err == nil && containsSegment(data) {
			close(s.ready)
			return
		}
		time.Sleep(120 * time.Millisecond)
	}
	s.err = fmt.Errorf("hls transcode did not start within %s", readyTimeout)
	if m.Log != nil {
		m.Log.Warn("rechls: session not ready", "sid", s.sid)
	}
	close(s.ready)
}

func containsSegment(playlist []byte) bool {
	// A playable playlist has at least one segment line.
	for i := 0; i+7 < len(playlist); i++ {
		if string(playlist[i:i+8]) == "#EXTINF:" {
			return true
		}
	}
	return false
}

// Playlist returns the absolute path to a session's playlist, touching it.
func (m *Manager) Playlist(sid string) (string, bool) {
	return m.file(sid, "index.m3u8")
}

// Segment returns the absolute path to a session segment, touching the session.
func (m *Manager) Segment(sid, name string) (string, bool) {
	return m.file(sid, name)
}

func (m *Manager) file(sid, name string) (string, bool) {
	m.mu.Lock()
	s, ok := m.sessions[sid]
	if ok {
		s.lastAccess = time.Now()
	}
	m.mu.Unlock()
	if !ok {
		return "", false
	}
	return filepath.Join(s.dir, name), true
}

func (m *Manager) drop(sid string) {
	m.mu.Lock()
	s, ok := m.sessions[sid]
	delete(m.sessions, sid)
	m.mu.Unlock()
	if ok {
		s.stop()
	}
}

func (s *session) stop() {
	if s.cancel != nil {
		s.cancel()
	}
	_ = os.RemoveAll(s.dir)
}

// evictLocked drops the least-recently-used session when at capacity. Caller
// holds m.mu.
func (m *Manager) evictLocked() {
	if len(m.sessions) < maxSessions {
		return
	}
	var oldest *session
	for _, s := range m.sessions {
		if oldest == nil || s.lastAccess.Before(oldest.lastAccess) {
			oldest = s
		}
	}
	if oldest != nil {
		delete(m.sessions, oldest.sid)
		go oldest.stop()
	}
}

// Run reaps idle sessions until ctx is cancelled, then tears everything down.
func (m *Manager) Run(ctx context.Context) {
	idle := m.IdleTimeout
	if idle <= 0 {
		idle = defaultIdle
	}
	t := time.NewTicker(gcInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.closeAll()
			return
		case <-t.C:
			now := time.Now()
			var stale []*session
			m.mu.Lock()
			for sid, s := range m.sessions {
				if now.Sub(s.lastAccess) > idle {
					stale = append(stale, s)
					delete(m.sessions, sid)
				}
			}
			m.mu.Unlock()
			for _, s := range stale {
				s.stop()
			}
		}
	}
}

// Close stops all sessions and removes the cache (for tests / explicit teardown;
// production relies on Run's ctx cancellation).
func (m *Manager) Close() { m.closeAll() }

func (m *Manager) closeAll() {
	m.mu.Lock()
	all := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.sessions = make(map[string]*session)
	m.mu.Unlock()
	for _, s := range all {
		s.stop()
	}
	_ = os.RemoveAll(m.Root)
}

// ffmpegArgs builds the file -> H.264 HLS transcode command. -ss before -i does
// a fast input seek so jump-to-offset playback starts immediately; the growing
// "event" playlist is seekable within what's been produced and becomes a normal
// VOD playlist (ENDLIST) once ffmpeg finishes.
func (m *Manager) ffmpegArgs(filePath string, fromSec int, dir string) []string {
	enc := hwaccel.Software()
	if m.EncoderFn != nil {
		if e := m.EncoderFn(); e != nil {
			enc = e
		}
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, enc.InputArgs(false)...)
	if fromSec > 0 {
		args = append(args, "-ss", strconv.Itoa(fromSec))
	}
	args = append(args, "-i", filePath)
	args = append(args, enc.VideoArgs(defaultBitrate, defaultGOP)...)
	// Force keyframes on segment boundaries so segments are clean and seekable.
	args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", segmentSeconds))
	args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "128k")
	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(segmentSeconds),
		"-hls_list_size", "0",
		"-hls_flags", "independent_segments+temp_file",
		"-hls_playlist_type", "event",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(dir, "seg%05d.ts"),
		filepath.Join(dir, "index.m3u8"),
	)
	return args
}
