// Command nvr is a pure-Go network video recorder: RTSP/RTMP ingest, ONVIF
// control, fMP4 recording with retention, S3 upload and a web UI.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/api"
	"github.com/AkagiYui/kenko-nvr/internal/config"
	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/logger"
	"github.com/AkagiYui/kenko-nvr/internal/manager"
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

	// Control plane: supervise cameras, ingest and consumers.
	mgr := manager.New(cfg, db, log)
	if err := mgr.Start(ctx); err != nil {
		log.Error("failed to start manager", "err", err)
		os.Exit(1)
	}
	defer mgr.Stop()

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
	srv := api.New(cfg, db, mgr, log)
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
