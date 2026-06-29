package manager

import (
	"context"
	"fmt"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/gb28181"
	"github.com/AkagiYui/kenko-nvr/internal/hwaccel"
	"github.com/AkagiYui/kenko-nvr/internal/rtmp"
	"github.com/AkagiYui/kenko-nvr/internal/rtspserver"
)

// service is one supervised background network server (RTMP ingest, RTSP
// re-publish, or GB28181 SIP). sig is the settings signature it was started for,
// so Apply can tell when a restart is needed.
type service struct {
	cancel context.CancelFunc
	done   chan struct{}
	sig    string
}

func (s *service) stop() {
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
}

// SystemConfig returns the effective runtime infrastructure config: the value
// stored in the database, or the built-in defaults if it has not been seeded yet.
func (m *Manager) SystemConfig() database.SystemConfig {
	sys, ok, _ := m.db.Settings.System()
	if !ok {
		return database.DefaultSystemConfig()
	}
	// Fill transcode defaults for configs persisted before transcode was a
	// runtime setting (and guard against zero/invalid values).
	if sys.Transcode.HWAccel == "" {
		sys.Transcode.HWAccel = "auto"
	}
	if sys.Transcode.LiveBitrateKbps <= 0 {
		sys.Transcode.LiveBitrateKbps = 2500
	}
	if sys.Transcode.LiveGOP <= 0 {
		sys.Transcode.LiveGOP = 50
	}
	return sys
}

// ApplySystemConfig (re)starts the supervised servers to match sys, restarting
// only those whose settings changed. WebRTC and the default RTSP transport are
// read live elsewhere, so they need no action here. Safe to call repeatedly.
func (m *Manager) ApplySystemConfig(sys database.SystemConfig) {
	m.svcMu.Lock()
	defer m.svcMu.Unlock()
	m.applyTranscode(sys.Transcode)
	m.applyRTMP(sys.RTMP)
	m.applyRTSPServer(sys.RTSPServer)
	m.applyGB28181(sys.GB28181)
}

// applyTranscode re-probes the live-transcode encoder when the hwaccel selection
// changes (the probe runs FFmpeg, so it is skipped when unchanged). Bitrate/GOP
// are read live per transcode session, so they need no action here. Caller holds
// svcMu. New transcode sessions pick up a new encoder; in-flight ones keep theirs.
func (m *Manager) applyTranscode(s database.TranscodeSettings) {
	if m.encProbed && m.encSig == s.HWAccel {
		return
	}
	enc := hwaccel.Detect(m.ctx, s.HWAccel, m.log)
	m.encMu.Lock()
	m.liveEncoder = enc
	m.encMu.Unlock()
	m.encSig = s.HWAccel
	m.encProbed = true
}

// liveEncoderGet returns the current live-transcode encoder under lock.
func (m *Manager) liveEncoderGet() *hwaccel.Encoder {
	m.encMu.Lock()
	defer m.encMu.Unlock()
	return m.liveEncoder
}

// run launches fn as a supervised goroutine tracked by m.wg, returning a service
// handle. Caller holds m.svcMu.
func (m *Manager) run(sig string, fn func(context.Context) error, name string) *service {
	ctx, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(done)
		if err := fn(ctx); err != nil && m.log != nil {
			m.log.Error(name+" stopped", "err", err)
		}
	}()
	return &service{cancel: cancel, done: done, sig: sig}
}

func (m *Manager) applyRTMP(s database.RTMPSettings) {
	sig := fmt.Sprintf("%t|%s", s.Enabled, s.Addr)
	if m.rtmpSvc != nil && m.rtmpSvc.sig == sig {
		return
	}
	if m.rtmpSvc != nil {
		m.rtmpSvc.stop()
		m.rtmpSvc = nil
	}
	if !s.Enabled {
		return
	}
	srv := &rtmp.Server{Addr: s.Addr, Log: m.log, Handler: m}
	m.rtmpSvc = m.run(sig, srv.Run, "rtmp server")
}

func (m *Manager) applyRTSPServer(s database.RTSPServerSettings) {
	sig := fmt.Sprintf("%t|%s", s.Enabled, s.Addr)
	if m.rtspSvc != nil && m.rtspSvc.sig == sig {
		return
	}
	if m.rtspSvc != nil {
		m.rtspSvc.stop()
		m.rtspSvc = nil
	}
	if !s.Enabled {
		return
	}
	srv := &rtspserver.Server{Addr: s.Addr, Provider: m, Log: m.log}
	m.rtspSvc = m.run(sig, srv.Run, "rtsp server")
}

func (m *Manager) applyGB28181(s database.GB28181Settings) {
	sig := gbSignature(s)
	if m.gbSvc != nil && m.gbSvc.sig == sig {
		return
	}
	if m.gbSvc != nil {
		m.gbSvc.stop()
		m.gbSvc = nil
		m.setGBServer(nil)
	}
	if s.Enabled {
		srv := gb28181.New(gb28181.Config{
			Enabled:      true,
			SIPAddr:      s.SIPAddr,
			ServerID:     s.ServerID,
			Domain:       s.Domain,
			Password:     s.Password,
			MediaIP:      s.MediaIP,
			MediaPortMin: s.MediaPortMin,
			MediaPortMax: s.MediaPortMax,
		}, m.log)
		m.setGBServer(srv)
		m.gbSvc = m.run(sig, srv.Run, "gb28181 server")
	}
	// The SIP platform was swapped (or torn down): reconnect every gb28181 camera
	// through it. Harmless at first startup (no cameras running yet).
	m.restartGB28181Cameras()
}

// restartGB28181Cameras (re)starts every running gb28181 camera so it binds to
// the current SIP platform. Caller may hold svcMu; this takes only m.mu.
func (m *Manager) restartGB28181Cameras() {
	m.mu.Lock()
	cams := make([]database.Camera, 0)
	for _, rt := range m.cams {
		if rt.camera.SourceType == database.SourceGB28181 {
			cams = append(cams, rt.camera)
		}
	}
	m.mu.Unlock()
	for _, cam := range cams {
		m.startCamera(cam)
	}
}

func gbSignature(s database.GB28181Settings) string {
	return fmt.Sprintf("%t|%s|%s|%s|%s|%s|%d|%d", s.Enabled, s.SIPAddr, s.ServerID,
		s.Domain, s.Password, s.MediaIP, s.MediaPortMin, s.MediaPortMax)
}

// setGBServer swaps the active GB28181 server under lock (read by buildPullSource
// and the API). nil means GB28181 is disabled.
func (m *Manager) setGBServer(s *gb28181.Server) {
	m.gbMu.Lock()
	m.gb = s
	m.gbMu.Unlock()
}

func (m *Manager) gb28181Server() *gb28181.Server {
	m.gbMu.Lock()
	defer m.gbMu.Unlock()
	return m.gb
}
