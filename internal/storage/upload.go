package storage

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// UploadStore is the subset of the recording store the upload worker uses.
type UploadStore interface {
	PendingUploads(limit int) ([]database.Recording, error)
	MarkUploaded(id, s3Key string) error
}

// UploadWorker periodically uploads completed-but-not-yet-uploaded recordings.
type UploadWorker struct {
	Root     string
	Store    UploadStore
	Log      *slog.Logger
	ConfigFn func() database.S3Config // live config
	// BatchSize bounds how many files are uploaded per tick.
	BatchSize int
}

// Run uploads pending recordings every interval until ctx is cancelled.
func (w *UploadWorker) Run(ctx context.Context, interval time.Duration) {
	if w.BatchSize <= 0 {
		w.BatchSize = 8
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *UploadWorker) runOnce(ctx context.Context) {
	cfg := w.ConfigFn()
	if !cfg.Enabled {
		return
	}
	uploader, err := NewUploader(cfg)
	if err != nil {
		if w.Log != nil {
			w.Log.Error("s3 uploader init failed", "err", err)
		}
		return
	}

	recs, err := w.Store.PendingUploads(w.BatchSize)
	if err != nil {
		if w.Log != nil {
			w.Log.Error("listing pending uploads", "err", err)
		}
		return
	}

	for _, rec := range recs {
		if ctx.Err() != nil {
			return
		}
		localPath := filepath.Join(w.Root, filepath.FromSlash(rec.Path))
		if _, err := os.Stat(localPath); err != nil {
			// File gone (deleted by retention); mark it handled so we stop
			// retrying a missing file.
			_ = w.Store.MarkUploaded(rec.ID, "")
			continue
		}

		key := uploader.Key(rec.Path)
		uctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		err := uploader.Upload(uctx, localPath, key)
		cancel()
		if err != nil {
			if w.Log != nil {
				w.Log.Error("s3 upload failed", "path", rec.Path, "err", err)
			}
			continue // retry next tick
		}

		if err := w.Store.MarkUploaded(rec.ID, key); err != nil && w.Log != nil {
			w.Log.Error("marking uploaded", "err", err)
		}
		if w.Log != nil {
			w.Log.Info("uploaded recording", "path", rec.Path, "key", key)
		}

		if cfg.DeleteLocalAfterUpload {
			if err := os.Remove(localPath); err != nil && w.Log != nil {
				w.Log.Warn("removing local file after upload", "err", err)
			}
		}
	}
}
