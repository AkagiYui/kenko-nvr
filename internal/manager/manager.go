// Package manager is the control plane: it supervises one runtime per camera,
// wiring media sources (RTSP pull / RTMP push) to the consumers (HLS live view
// and the fMP4 recorder), and handles reconnection.
package manager

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/AkagiYui/kenko-nvr/internal/config"
	"github.com/AkagiYui/kenko-nvr/internal/core"
	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/rtmp"
	"github.com/AkagiYui/kenko-nvr/internal/rtsp"
)

// Manager supervises all camera runtimes.
type Manager struct {
	cfg config.Config
	db  *database.DB
	log *slog.Logger

	recordingsRoot string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu   sync.Mutex
	cams map[string]*camRuntime

	rtmpServer *rtmp.Server
}

// New creates a Manager.
func New(cfg config.Config, db *database.DB, log *slog.Logger) *Manager {
	return &Manager{
		cfg:            cfg,
		db:             db,
		log:            log,
		recordingsRoot: cfg.Storage.RecordingsDir,
		cams:           make(map[string]*camRuntime),
	}
}

// Start launches the RTMP server (if enabled) and every enabled camera.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx, m.cancel = context.WithCancel(ctx)

	if m.cfg.RTMP.Enabled {
		m.rtmpServer = &rtmp.Server{
			Addr:    m.cfg.RTMP.Addr,
			Log:     m.log,
			Handler: m,
		}
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if err := m.rtmpServer.Run(m.ctx); err != nil {
				m.log.Error("rtmp server stopped", "err", err)
			}
		}()
	}

	cams, err := m.db.Cameras.List()
	if err != nil {
		return err
	}
	for _, cam := range cams {
		if cam.Enabled {
			m.startCamera(cam)
		}
	}
	return nil
}

// Stop tears down all runtimes and waits for them to exit.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	for _, rt := range m.cams {
		rt.stop()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// --- camera lifecycle ---------------------------------------------------------

// startCamera creates and starts a runtime. Caller must not hold m.mu.
func (m *Manager) startCamera(cam database.Camera) {
	m.mu.Lock()
	if existing, ok := m.cams[cam.ID]; ok {
		existing.stop()
		delete(m.cams, cam.ID)
	}
	rt := &camRuntime{
		mgr:    m,
		camera: cam,
		state:  core.StateIdle,
	}
	m.cams[cam.ID] = rt
	m.mu.Unlock()

	rt.start(m.ctx)
}

// ApplyCamera (re)starts or stops a camera after a config change.
func (m *Manager) ApplyCamera(cam database.Camera) {
	if cam.Enabled {
		m.startCamera(cam)
		return
	}
	m.RemoveCamera(cam.ID)
}

// RemoveCamera stops and forgets a camera runtime.
func (m *Manager) RemoveCamera(id string) {
	m.mu.Lock()
	rt, ok := m.cams[id]
	if ok {
		delete(m.cams, id)
	}
	m.mu.Unlock()
	if ok {
		rt.stop()
	}
}

func (m *Manager) runtime(id string) *camRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cams[id]
}

// --- HLS serving --------------------------------------------------------------

// ServeHLS serves the live HLS playlist/segments for a camera, if it is live.
func (m *Manager) ServeHLS(id string, w http.ResponseWriter, r *http.Request) {
	rt := m.runtime(id)
	if rt == nil {
		http.Error(w, "camera not running", http.StatusNotFound)
		return
	}
	mux := rt.hlsMuxer()
	if mux == nil {
		http.Error(w, "stream not available", http.StatusServiceUnavailable)
		return
	}
	mux.ServeHTTP(w, r)
}

// --- status -------------------------------------------------------------------

// CameraStatus is the live status of a camera for the UI.
type CameraStatus struct {
	ID        string      `json:"id"`
	State     string      `json:"state"`
	Error     string      `json:"error,omitempty"`
	Live      bool        `json:"live"`
	Recording bool        `json:"recording"`
	Tracks    []TrackInfo `json:"tracks,omitempty"`
}

// TrackInfo describes a live track for the UI.
type TrackInfo struct {
	Kind  string `json:"kind"`
	Codec string `json:"codec"`
}

// Status returns the live status of one camera.
func (m *Manager) Status(id string) CameraStatus {
	rt := m.runtime(id)
	if rt == nil {
		return CameraStatus{ID: id, State: string(core.StateIdle)}
	}
	return rt.status()
}

// AllStatus returns the status of every running camera.
func (m *Manager) AllStatus() map[string]CameraStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]CameraStatus, len(m.cams))
	for id, rt := range m.cams {
		out[id] = rt.status()
	}
	return out
}

// --- recording.Sink -----------------------------------------------------------

// SegmentStarted records a new in-progress recording row.
func (m *Manager) SegmentStarted(cameraID, relPath string, start time.Time) (string, error) {
	id := uuid.NewString()
	err := m.db.Recordings.Create(database.Recording{
		ID:        id,
		CameraID:  cameraID,
		Path:      relPath,
		StartTime: start,
		CreatedAt: time.Now(),
	})
	return id, err
}

// SegmentFinalized marks a recording complete.
func (m *Manager) SegmentFinalized(recordingID string, end time.Time, durationMS, sizeBytes int64) error {
	return m.db.Recordings.Finalize(recordingID, end, durationMS, sizeBytes)
}

// --- rtmp.PublishHandler ------------------------------------------------------

// OnPublishStart attaches an incoming RTMP push to its camera (matched by
// stream key == camera ID).
func (m *Manager) OnPublishStart(streamKey string, stream *core.Stream) bool {
	rt := m.runtime(streamKey)
	if rt == nil || rt.camera.SourceType != database.SourceRTMP {
		m.log.Warn("rejecting rtmp publish: no matching rtmp camera", "key", streamKey)
		return false
	}
	rt.attachPush(stream)
	m.log.Info("rtmp push attached", "camera", streamKey)
	return true
}

// OnPublishStop detaches an RTMP push.
func (m *Manager) OnPublishStop(streamKey string) {
	if rt := m.runtime(streamKey); rt != nil {
		rt.detachPush()
	}
}

// recordingConfig snapshots the current recording settings.
func (m *Manager) recordingConfig() database.RecordingConfig {
	rc, err := m.db.Settings.Recording()
	if err != nil {
		return database.DefaultRecordingConfig()
	}
	return rc
}

// buildPullSource constructs the appropriate core.Source for a pull camera.
func (m *Manager) buildPullSource(cam database.Camera) core.Source {
	switch cam.SourceType {
	case database.SourceRTSP:
		transport := cam.Transport
		if transport == "" && m.cfg.RTSP.Transport != "automatic" {
			transport = m.cfg.RTSP.Transport
		}
		return &rtsp.Source{
			URL:       cam.URL,
			Username:  cam.Username,
			Password:  cam.Password,
			Transport: transport,
			Log:       m.log,
		}
	default:
		return nil
	}
}
