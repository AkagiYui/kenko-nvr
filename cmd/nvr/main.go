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
	"github.com/AkagiYui/kenko-nvr/internal/gb28181"
	"github.com/AkagiYui/kenko-nvr/internal/hadiscovery"
	"github.com/AkagiYui/kenko-nvr/internal/hwaccel"
	"github.com/AkagiYui/kenko-nvr/internal/logger"
	"github.com/AkagiYui/kenko-nvr/internal/manager"
	"github.com/AkagiYui/kenko-nvr/internal/notify"
	"github.com/AkagiYui/kenko-nvr/internal/recording"
	"github.com/AkagiYui/kenko-nvr/internal/rtspserver"
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

	// Discover the live-transcode encoder for this machine (hardware if usable,
	// else software). This probes FFmpeg once at startup; the deployer configures
	// nothing beyond an optional override.
	enc := hwaccel.Detect(ctx, cfg.Transcode.HWAccel, log)

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

	// Control plane: supervise cameras, ingest and consumers.
	mgr := manager.New(cfg, db, log)
	mgr.SetLiveEncoder(enc)
	mgr.SetNotifier(notifier)

	// GB28181 SIP platform (optional): IP cameras / NVRs register here and their
	// channels can be added as cameras of source type "gb28181".
	if cfg.GB28181.Enabled {
		gbSrv := gb28181.New(gb28181.Config{
			Enabled:      true,
			SIPAddr:      cfg.GB28181.SIPAddr,
			ServerID:     cfg.GB28181.ServerID,
			Domain:       cfg.GB28181.Domain,
			Password:     cfg.GB28181.Password,
			MediaIP:      cfg.GB28181.MediaIP,
			MediaPortMin: cfg.GB28181.MediaPortMin,
			MediaPortMax: cfg.GB28181.MediaPortMax,
		}, log)
		mgr.SetGB28181(gbSrv)
		go func() {
			if err := gbSrv.Run(ctx); err != nil {
				log.Error("gb28181 server stopped", "err", err)
			}
		}()
	}

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

	// RTSP re-publishing server: external clients pull rtsp://host/<cameraID>.
	if cfg.RTSPServer.Enabled {
		rtspSrv := &rtspserver.Server{Addr: cfg.RTSPServer.Addr, Provider: mgr, Log: log}
		go func() {
			if err := rtspSrv.Run(ctx); err != nil {
				log.Error("rtsp server stopped", "err", err)
			}
		}()
	}

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

	// Management/API/HLS server (blocks until shutdown).
	srv := api.New(cfg, db, mgr, notifier, log)
	log.Info("kenko-nvr started",
		"http", cfg.HTTP.Addr,
		"rtmp", cfg.RTMP.Addr,
		"recordings", cfg.Storage.RecordingsDir,
	)
	if err := srv.Run(ctx); err != nil {
		log.Error("http server error", "err", err)
	}

	log.Info("shutting down")
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
