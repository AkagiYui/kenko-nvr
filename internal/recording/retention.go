package recording

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// RetentionStore is the subset of the recording store the retention worker uses.
type RetentionStore interface {
	OldestComplete(limit int, onlyUploaded bool) ([]database.Recording, error)
	TotalSize() (int64, error)
	Delete(id string) error
	// MarkLocalRemoved keeps the row but flags the local file as deleted, used
	// when a recording is preserved on S3.
	MarkLocalRemoved(id string) error
}

// Retention enforces rolling deletion of recordings by age, total size and free
// disk space.
type Retention struct {
	Root  string
	Store RetentionStore
	Log   *slog.Logger
	// DiskUsage reports free and total bytes for the filesystem at path. It is
	// a field so tests can substitute a deterministic implementation.
	DiskUsage func(path string) (free, total uint64, err error)
}

const gib = 1 << 30

// Run enforces the policy every interval until ctx is cancelled. policyFn is
// called each tick so live edits to the policy take effect immediately.
func (r *Retention) Run(ctx context.Context, interval time.Duration, policyFn func() (database.RetentionPolicy, bool)) {
	if r.DiskUsage == nil {
		r.DiskUsage = diskUsage
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			policy, s3Enabled := policyFn()
			if !policy.Enabled {
				continue
			}
			if n, err := r.Enforce(policy, s3Enabled); err != nil {
				if r.Log != nil {
					r.Log.Error("retention error", "err", err)
				}
			} else if n > 0 && r.Log != nil {
				r.Log.Info("retention deleted recordings", "count", n)
			}
		}
	}
}

// Enforce runs a single retention pass and returns the number of recordings
// deleted. It is the testable core of the worker.
func (r *Retention) Enforce(policy database.RetentionPolicy, s3Enabled bool) (int, error) {
	onlyUploaded := policy.DeleteAfterUpload && s3Enabled
	deleted := 0

	// 1) Age-based deletion.
	if policy.MaxAgeDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -policy.MaxAgeDays)
		recs, err := r.Store.OldestComplete(10000, onlyUploaded)
		if err != nil {
			return deleted, err
		}
		for _, rec := range recs {
			if rec.StartTime.After(cutoff) {
				break // OldestComplete is ascending; the rest are newer
			}
			if err := r.delete(rec, s3Enabled); err != nil {
				return deleted, err
			}
			deleted++
		}
	}

	// 2) Total-size cap.
	if policy.MaxTotalSizeGB > 0 {
		limit := int64(policy.MaxTotalSizeGB * gib)
		n, err := r.deleteUntil(onlyUploaded, s3Enabled, func() (bool, error) {
			total, err := r.Store.TotalSize()
			if err != nil {
				return false, err
			}
			return total > limit, nil
		})
		if err != nil {
			return deleted, err
		}
		deleted += n
	}

	// 3) Free-space floor.
	if policy.MinFreeSpaceGB > 0 {
		floor := uint64(policy.MinFreeSpaceGB * gib)
		n, err := r.deleteUntil(onlyUploaded, s3Enabled, func() (bool, error) {
			free, _, err := r.DiskUsage(r.Root)
			if err != nil {
				return false, err
			}
			return free < floor, nil
		})
		if err != nil {
			return deleted, err
		}
		deleted += n
	}

	return deleted, nil
}

// deleteUntil deletes oldest recordings one at a time while need() is true.
func (r *Retention) deleteUntil(onlyUploaded, s3Enabled bool, need func() (bool, error)) (int, error) {
	deleted := 0
	for {
		over, err := need()
		if err != nil {
			return deleted, err
		}
		if !over {
			return deleted, nil
		}
		recs, err := r.Store.OldestComplete(1, onlyUploaded)
		if err != nil {
			return deleted, err
		}
		if len(recs) == 0 {
			return deleted, nil // nothing left we are allowed to delete
		}
		if err := r.delete(recs[0], s3Enabled); err != nil {
			return deleted, err
		}
		deleted++
	}
}

// delete frees a recording's local file. When the recording is already archived
// to S3 (and S3 is enabled) the database row is kept and only flagged
// local_removed, so the clip stays listed and remains playable by streaming it
// back from S3 through the NVR. Otherwise the row is removed entirely.
func (r *Retention) delete(rec database.Recording, s3Enabled bool) error {
	abs := filepath.Join(r.Root, filepath.FromSlash(rec.Path))
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return err
	}
	removeEmptyParents(r.Root, filepath.Dir(abs))
	if s3Enabled && rec.Uploaded && rec.S3Key != "" {
		return r.Store.MarkLocalRemoved(rec.ID)
	}
	return r.Store.Delete(rec.ID)
}

// removeEmptyParents prunes empty directories from dir up to (not including)
// root, best-effort.
func removeEmptyParents(root, dir string) {
	root = filepath.Clean(root)
	for {
		dir = filepath.Clean(dir)
		if dir == root || dir == "." || dir == string(filepath.Separator) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
