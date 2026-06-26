// Package api exposes the REST/WebSocket management interface and serves the
// embedded web UI and live HLS.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/AkagiYui/kenko-nvr/internal/config"
	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/manager"
	"github.com/AkagiYui/kenko-nvr/internal/notify"
	webui "github.com/AkagiYui/kenko-nvr/internal/web"
)

// Server is the HTTP server.
type Server struct {
	cfg      config.Config
	db       *database.DB
	mgr      *manager.Manager
	log      *slog.Logger
	auth     *authenticator
	notifier *notify.Notifier
	http     *http.Server
}

// New creates the API server.
func New(cfg config.Config, db *database.DB, mgr *manager.Manager, notifier *notify.Notifier, log *slog.Logger) *Server {
	// Seed the first admin from bootstrap config so a fresh install can log in.
	if err := ensureSeedAdmin(db, cfg.HTTP.Username, cfg.HTTP.Password); err != nil {
		log.Error("failed to seed admin user", "err", err)
	}
	s := &Server{
		cfg:      cfg,
		db:       db,
		mgr:      mgr,
		log:      log,
		auth:     newAuthenticator(db.Users),
		notifier: notifier,
	}
	s.http = &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           s.router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

func (s *Server) router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	r.Route("/api", func(r chi.Router) {
		r.Post("/login", s.handleLogin)

		// Authenticated endpoints.
		r.Group(func(r chi.Router) {
			r.Use(s.auth.middleware)

			// Identity (any authenticated user).
			r.Get("/me", s.handleMe)
			r.Post("/logout", s.handleLogout)

			// Read-only views (viewer and up).
			r.Get("/cameras", s.handleListCameras)
			r.Get("/cameras/{id}", s.handleGetCamera)
			r.Get("/cameras/{id}/status", s.handleCameraStatus)
			r.Get("/status", s.handleAllStatus)
			r.Get("/cameras/{id}/ptz/presets", s.handlePTZPresets)
			r.Get("/recordings", s.handleListRecordings)
			r.Get("/recordings/{id}", s.handleGetRecording)
			r.Get("/events", s.handleListEvents)
			r.Get("/settings/videowall", s.handleGetVideoWall)
			r.Get("/gb28181/info", s.handleGB28181Info)
			r.Get("/gb28181/devices", s.handleGB28181Devices)

			// Anyone authenticated can register their browser for push.
			r.Get("/notifications/vapid", s.handleVAPIDPublicKey)
			r.Post("/notifications/subscribe", s.handleSubscribePush)

			// Operator and up: manage cameras and control them.
			r.Group(func(r chi.Router) {
				r.Use(requireRole(database.RoleOperator))
				r.Post("/cameras", s.handleCreateCamera)
				r.Put("/cameras/{id}", s.handleUpdateCamera)
				r.Delete("/cameras/{id}", s.handleDeleteCamera)
				r.Post("/cameras/{id}/ptz", s.handlePTZ)
				r.Get("/onvif/discover", s.handleOnvifDiscover)
				r.Post("/onvif/probe", s.handleOnvifProbe)
				r.Delete("/recordings/{id}", s.handleDeleteRecording)
				r.Put("/settings/videowall", s.handleSetVideoWall)
				r.Post("/gb28181/devices/{deviceId}/refresh", s.handleGB28181Refresh)
			})

			// Admin only: users and system settings.
			r.Group(func(r chi.Router) {
				r.Use(requireRole(database.RoleAdmin))

				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateUser)
				r.Put("/users/{id}", s.handleUpdateUser)
				r.Delete("/users/{id}", s.handleDeleteUser)

				r.Get("/settings/retention", s.handleGetRetention)
				r.Put("/settings/retention", s.handleSetRetention)
				r.Get("/settings/s3", s.handleGetS3)
				r.Put("/settings/s3", s.handleSetS3)
				r.Post("/settings/s3/test", s.handleTestS3)
				r.Get("/settings/recording", s.handleGetRecordingCfg)
				r.Put("/settings/recording", s.handleSetRecordingCfg)
				r.Get("/settings/notifications", s.handleGetNotifications)
				r.Put("/settings/notifications", s.handleSetNotifications)
				r.Post("/settings/notifications/test", s.handleTestNotification)
				r.Get("/settings/homeassistant", s.handleGetHA)
				r.Put("/settings/homeassistant", s.handleSetHA)
			})
		})

		// Media + WebSocket endpoints: authenticated, but tokens may arrive via
		// the ?token= query parameter, since <video>/hls.js and the browser
		// WebSocket API cannot set custom request headers.
		r.Group(func(r chi.Router) {
			r.Use(s.auth.mediaMiddleware)
			r.Get("/cameras/{id}/hls/*", s.handleHLS)
			r.Get("/cameras/{id}/mse", s.handleMSE)
			r.Get("/cameras/{id}/flv", s.handleFLV)
			r.Get("/cameras/{id}/flv.ws", s.handleFLVWS)
			r.Get("/cameras/{id}/ts", s.handleTS)
			r.Post("/cameras/{id}/webrtc", s.handleWebRTC)
			r.Get("/cameras/{id}/talk", s.handleTalk)
			r.Get("/recordings/{id}/file", s.handleRecordingFile)
			r.Get("/ws", s.handleWS)
		})
	})

	// Static web UI (embedded SPA with client-side routing fallback).
	r.Handle("/*", webui.Handler())
	return r
}

// Run starts the server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", "addr", s.cfg.HTTP.Addr)
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.http.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// --- helpers ------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(dst)
}
