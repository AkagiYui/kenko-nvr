// Command nvr is a pure-Go network video recorder: RTSP/RTMP ingest, ONVIF
// control, fMP4 recording with retention, S3 upload and a web UI.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/api"
	"github.com/AkagiYui/kenko-nvr/internal/config"
	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/face"
	"github.com/AkagiYui/kenko-nvr/internal/hadiscovery"
	"github.com/AkagiYui/kenko-nvr/internal/logger"
	"github.com/AkagiYui/kenko-nvr/internal/manager"
	"github.com/AkagiYui/kenko-nvr/internal/notify"
	"github.com/AkagiYui/kenko-nvr/internal/recording"
	"github.com/AkagiYui/kenko-nvr/internal/storage"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		println("kenko-nvr", version)
		return
	}

	cfg, err := config.Load(*configPath)
	log := logger.Setup(cfg.Log.Level)
	if err != nil {
		log.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	db, err := database.Open(cfg.Storage.DBPath)
	if err != nil {
		log.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := os.MkdirAll(cfg.Storage.RecordingsDir, 0o755); err != nil {
		log.Error("failed to create recordings dir", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Notifier delivers motion / offline alerts to the configured channels.
	notifier := &notify.Notifier{
		ConfigFn: func() database.NotificationConfig {
			c, _ := db.Settings.Notifications()
			return c
		},
		Push: db.Push,
		Log:  log,
	}
	defer notifier.Close()

	// Control plane: supervise cameras, ingest and consumers. The live-transcode
	// encoder is probed by the manager from the runtime transcode settings.
	mgr := manager.New(cfg, db, log)
	mgr.SetNotifier(notifier)

	// Seed the runtime infrastructure config (RTMP / RTSP / RTSP-server / WebRTC /
	// GB28181) from the YAML bootstrap on first run; thereafter the database is the
	// source of truth and these are edited live in the web UI.
	seedSystemConfig(db, log)

	// The manager owns the supervised network servers (RTMP ingest, RTSP
	// re-publish, GB28181 SIP) and (re)starts them from the runtime config.
	if err := mgr.Start(ctx); err != nil {
		log.Error("failed to start manager", "err", err)
		os.Exit(1)
	}
	defer mgr.Stop()

	// Home Assistant MQTT discovery (optional): publishes each camera as an HA
	// device (motion binary_sensor + availability) over the configured MQTT broker.
	haPub := &hadiscovery.Publisher{
		DB:       db,
		Notifier: notifier,
		Status:   mgr,
		Log:      log,
	}
	mgr.SetHADiscovery(haPub)
	go haPub.Run(ctx)

	// Periodically prune old motion events (mirror the recordings age limit).
	go runEventCleanup(ctx, db, log)

	// Retention worker (rolling deletion / storage thresholds).
	ret := &recording.Retention{
		Root:  cfg.Storage.RecordingsDir,
		Store: db.Recordings,
		Log:   log,
	}
	go ret.Run(ctx, time.Minute, func() (database.RetentionPolicy, bool) {
		policy, _ := db.Settings.Retention()
		s3cfg, _ := db.Settings.S3()
		return policy, s3cfg.Enabled
	})

	// S3 upload worker.
	uploader := &storage.UploadWorker{
		Root:  cfg.Storage.RecordingsDir,
		Store: db.Recordings,
		Log:   log,
		ConfigFn: func() database.S3Config {
			c, _ := db.Settings.S3()
			return c
		},
	}
	go uploader.Run(ctx, 30*time.Second)

	// Face-recognition worker: drains the post-process queue (sample frames ->
	// sidecar inference -> store faces -> group into identities). It idles unless
	// face recognition is enabled in settings.
	faceWorker := &face.Worker{
		DB:         db,
		Root:       cfg.Storage.RecordingsDir,
		FacesDir:   cfg.Storage.FacesDir,
		FFmpegPath: "ffmpeg",
		ConfigFn: func() database.FaceConfig {
			c, _ := db.Settings.Face()
			return c
		},
		Assigner: &face.Gallery{DB: db, Log: log},
		Log:      log,
	}
	go faceWorker.Run(ctx, 5*time.Second)

	// Periodic global re-clustering: cleans up the over-splitting incremental
	// assignment leaves behind, honouring operator confirmations and links.
	go func() {
		clus := &face.Clusterer{DB: db, Log: log}
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if c, _ := db.Settings.Face(); c.Enabled {
					if _, err := clus.Recluster(ctx, c); err != nil {
						log.Warn("face: recluster failed", "err", err)
					}
				}
			}
		}
	}()

	// Management/API/HLS server (blocks until shutdown).
	srv := api.New(cfg, db, mgr, notifier, log)
	log.Info("kenko-nvr started",
		"http", cfg.HTTP.Addr,
		"recordings", cfg.Storage.RecordingsDir,
	)
	if err := srv.Run(ctx); err != nil {
		log.Error("http server error", "err", err)
	}

	log.Info("shutting down")
}

// seedSystemConfig writes the built-in infrastructure defaults into the database
// the first time the server runs, so the web UI has a concrete starting point to
// edit. On later runs the stored config wins.
func seedSystemConfig(db *database.DB, log *slog.Logger) {
	if _, ok, err := db.Settings.System(); ok || err != nil {
		return
	}
	if err := db.Settings.SetSystem(database.DefaultSystemConfig()); err != nil {
		log.Warn("failed to seed system config", "err", err)
	}
}

// runEventCleanup periodically deletes motion events older than the recordings
// retention age (default 30 days), so the events table does not grow unbounded.
func runEventCleanup(ctx context.Context, db *database.DB, log *slog.Logger) {
	clean := func() {
		days := 30
		if p, err := db.Settings.Retention(); err == nil && p.MaxAgeDays > 0 {
			days = p.MaxAgeDays
		}
		cutoff := time.Now().AddDate(0, 0, -days)
		if n, err := db.Events.DeleteOlderThan(cutoff); err == nil && n > 0 {
			log.Info("pruned old events", "count", n)
		}
	}
	clean()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			clean()
		}
	}
}
