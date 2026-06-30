package recording

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

type fakeStore struct {
	recs         []database.Recording // kept sorted ascending by StartTime
	totalSize    int64
	deleted      []string
	localRemoved []string
}

func (f *fakeStore) OldestComplete(limit int, onlyUploaded bool) ([]database.Recording, error) {
	var out []database.Recording
	for _, r := range f.recs {
		if r.LocalRemoved {
			continue // no local file left to free
		}
		if onlyUploaded && !r.Uploaded {
			continue
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) TotalSize() (int64, error) { return f.totalSize, nil }

func (f *fakeStore) Delete(id string) error {
	for i, r := range f.recs {
		if r.ID == id {
			if !r.LocalRemoved {
				f.totalSize -= r.SizeBytes
			}
			f.recs = append(f.recs[:i], f.recs[i+1:]...)
			f.deleted = append(f.deleted, id)
			return nil
		}
	}
	return nil
}

// MarkLocalRemoved flags the row's local file as gone but keeps the row, mirroring
// the real store: it stops counting toward local disk usage.
func (f *fakeStore) MarkLocalRemoved(id string) error {
	for i := range f.recs {
		if f.recs[i].ID == id {
			if !f.recs[i].LocalRemoved {
				f.totalSize -= f.recs[i].SizeBytes
			}
			f.recs[i].LocalRemoved = true
			f.localRemoved = append(f.localRemoved, id)
			return nil
		}
	}
	return nil
}

// seed creates real files under root and a matching fake store.
func seed(t *testing.T, root string, recs []database.Recording) *fakeStore {
	t.Helper()
	fs := &fakeStore{}
	for _, r := range recs {
		abs := filepath.Join(root, filepath.FromSlash(r.Path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, make([]byte, 16), 0o644); err != nil {
			t.Fatal(err)
		}
		fs.recs = append(fs.recs, r)
		fs.totalSize += r.SizeBytes
	}
	return fs
}

func TestRetentionByAge(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	fs := seed(t, root, []database.Recording{
		{ID: "old1", Path: "c/old1.mp4", StartTime: database.MS(now.AddDate(0, 0, -40)), SizeBytes: 100, Complete: true},
		{ID: "old2", Path: "c/old2.mp4", StartTime: database.MS(now.AddDate(0, 0, -31)), SizeBytes: 100, Complete: true},
		{ID: "new1", Path: "c/new1.mp4", StartTime: database.MS(now.AddDate(0, 0, -5)), SizeBytes: 100, Complete: true},
	})
	r := &Retention{Root: root, Store: fs}

	n, err := r.Enforce(database.RetentionPolicy{Enabled: true, MaxAgeDays: 30}, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 deletions, got %d", n)
	}
	if len(fs.recs) != 1 || fs.recs[0].ID != "new1" {
		t.Errorf("expected only new1 to remain, got %+v", fs.recs)
	}
	// files actually removed
	if _, err := os.Stat(filepath.Join(root, "c/old1.mp4")); !os.IsNotExist(err) {
		t.Error("old1 file should be deleted")
	}
}

func TestRetentionByTotalSize(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	oneGB := int64(gib)
	fs := seed(t, root, []database.Recording{
		{ID: "a", Path: "a.mp4", StartTime: database.MS(now.Add(-3 * time.Hour)), SizeBytes: oneGB, Complete: true},
		{ID: "b", Path: "b.mp4", StartTime: database.MS(now.Add(-2 * time.Hour)), SizeBytes: oneGB, Complete: true},
		{ID: "c", Path: "c.mp4", StartTime: database.MS(now.Add(-1 * time.Hour)), SizeBytes: oneGB, Complete: true},
	})
	r := &Retention{Root: root, Store: fs}

	// cap at 1.5 GB -> must delete the two oldest, leaving "c"
	n, err := r.Enforce(database.RetentionPolicy{Enabled: true, MaxTotalSizeGB: 1.5}, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 deletions, got %d", n)
	}
	if len(fs.recs) != 1 || fs.recs[0].ID != "c" {
		t.Errorf("expected only newest to remain, got %+v", fs.recs)
	}
}

func TestRetentionByFreeSpace(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	fs := seed(t, root, []database.Recording{
		{ID: "a", Path: "a.mp4", StartTime: database.MS(now.Add(-3 * time.Hour)), SizeBytes: gib, Complete: true},
		{ID: "b", Path: "b.mp4", StartTime: database.MS(now.Add(-2 * time.Hour)), SizeBytes: gib, Complete: true},
		{ID: "c", Path: "c.mp4", StartTime: database.MS(now.Add(-1 * time.Hour)), SizeBytes: gib, Complete: true},
	})
	startTotal := fs.totalSize
	freeStart := uint64(2 * gib) // below a 4GB floor

	r := &Retention{
		Root:  root,
		Store: fs,
		DiskUsage: func(string) (uint64, uint64, error) {
			// free grows as recordings are deleted
			reclaimed := uint64(startTotal - fs.totalSize)
			return freeStart + reclaimed, 100 * gib, nil
		},
	}

	// floor 4GB: start free=2GB, each delete frees 1GB -> need 2 deletions
	n, err := r.Enforce(database.RetentionPolicy{Enabled: true, MinFreeSpaceGB: 4}, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 deletions to reach free-space floor, got %d", n)
	}
}

func TestRetentionDeleteAfterUploadOnly(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	fs := seed(t, root, []database.Recording{
		{ID: "notup", Path: "notup.mp4", StartTime: database.MS(now.AddDate(0, 0, -40)), SizeBytes: 100, Complete: true, Uploaded: false},
		{ID: "up", Path: "up.mp4", StartTime: database.MS(now.AddDate(0, 0, -39)), SizeBytes: 100, Complete: true, Uploaded: true, S3Key: "cam/up.mp4"},
	})
	r := &Retention{Root: root, Store: fs}

	// onlyUploaded=true (s3 enabled): must skip the un-uploaded old file and free
	// only the uploaded one.
	n, err := r.Enforce(database.RetentionPolicy{Enabled: true, MaxAgeDays: 30, DeleteAfterUpload: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 deletion (uploaded only), got %d", n)
	}
	// The uploaded clip is preserved on S3: its row is kept but flagged
	// local_removed and its local file is gone.
	var up *database.Recording
	for i := range fs.recs {
		if fs.recs[i].ID == "up" {
			up = &fs.recs[i]
		}
	}
	if up == nil {
		t.Fatal("uploaded recording row should be kept for S3 playback")
	}
	if !up.LocalRemoved {
		t.Error("uploaded recording should be flagged local_removed")
	}
	if _, err := os.Stat(filepath.Join(root, "up.mp4")); !os.IsNotExist(err) {
		t.Error("uploaded recording's local file should be deleted")
	}
}

// TestRetentionPreserveVsHardDelete checks the two delete paths: an uploaded
// clip with an S3 key is preserved (row kept, flagged), while one without a key
// (uploaded edge case / S3 disabled) is hard-deleted.
func TestRetentionPreserveVsHardDelete(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	// S3 enabled: uploaded+key is preserved.
	fs := seed(t, root, []database.Recording{
		{ID: "a", Path: "a.mp4", StartTime: database.MS(now.AddDate(0, 0, -40)), SizeBytes: 100, Complete: true, Uploaded: true, S3Key: "cam/a.mp4"},
	})
	r := &Retention{Root: root, Store: fs}
	if _, err := r.Enforce(database.RetentionPolicy{Enabled: true, MaxAgeDays: 30}, true); err != nil {
		t.Fatal(err)
	}
	if len(fs.recs) != 1 || !fs.recs[0].LocalRemoved {
		t.Errorf("expected row preserved + local_removed, got %+v", fs.recs)
	}
	if len(fs.deleted) != 0 {
		t.Errorf("expected no hard delete, got %v", fs.deleted)
	}

	// S3 disabled: same clip is hard-deleted (no cloud copy to keep playable).
	fs2 := seed(t, root, []database.Recording{
		{ID: "b", Path: "b.mp4", StartTime: database.MS(now.AddDate(0, 0, -40)), SizeBytes: 100, Complete: true, Uploaded: true, S3Key: "cam/b.mp4"},
	})
	r2 := &Retention{Root: root, Store: fs2}
	if _, err := r2.Enforce(database.RetentionPolicy{Enabled: true, MaxAgeDays: 30}, false); err != nil {
		t.Fatal(err)
	}
	if len(fs2.recs) != 0 {
		t.Errorf("expected hard delete when s3 disabled, got %+v", fs2.recs)
	}
}
