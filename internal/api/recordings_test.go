package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/config"
	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/storage"
)

// memObject is an in-memory io.ReadSeekCloser for faking S3 objects in tests.
type memObject struct{ *bytes.Reader }

func (memObject) Close() error { return nil }

type fakeArchive struct {
	data map[string][]byte
	err  error
}

func (f fakeArchive) Open(_ context.Context, key string, _ bool) (*storage.Object, error) {
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.data[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return &storage.Object{Body: memObject{bytes.NewReader(b)}, Size: int64(len(b)), ModTime: time.Unix(0, 0)}, nil
}

// serveRecording invokes handleRecordingFile with the chi "id" URL param set.
func serveRecording(s *Server, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/recordings/"+id+"/file", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
	rr := httptest.NewRecorder()
	s.handleRecordingFile(rr, r)
	return rr
}

func TestRecordingFilePlayback(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Cameras.Create(database.Camera{ID: "c", Name: "c", SourceType: database.SourceRTSP}); err != nil {
		t.Fatal(err)
	}

	localBody := []byte("LOCAL-MP4-DATA")
	s3Body := []byte("S3-ARCHIVED-MP4-DATA")

	// local: file present on disk.
	if err := os.MkdirAll(filepath.Join(root, "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "c", "local.mp4"), localBody, 0o644); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, db, database.Recording{ID: "local", CameraID: "c", Path: "c/local.mp4", Complete: true})

	// cloud: uploaded then removed locally (no file on disk).
	mustCreate(t, db, database.Recording{
		ID: "cloud", CameraID: "c", Path: "c/cloud.mp4", Complete: true,
		Uploaded: true, S3Key: "bucket/c/cloud.mp4", LocalRemoved: true,
	})

	// gone: removed locally and never uploaded.
	mustCreate(t, db, database.Recording{ID: "gone", CameraID: "c", Path: "c/gone.mp4", Complete: true})

	newServer := func(arch recordingArchive) *Server {
		return &Server{
			cfg:     config.Config{Storage: config.StorageConfig{RecordingsDir: root}},
			db:      db,
			log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			archive: arch,
		}
	}

	t.Run("serves local file", func(t *testing.T) {
		s := newServer(fakeArchive{})
		rr := serveRecording(s, "local")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		if !bytes.Equal(rr.Body.Bytes(), localBody) {
			t.Errorf("body = %q, want local data", rr.Body.Bytes())
		}
	})

	t.Run("falls back to S3 when local gone", func(t *testing.T) {
		s := newServer(fakeArchive{data: map[string][]byte{"bucket/c/cloud.mp4": s3Body}})
		rr := serveRecording(s, "cloud")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		if !bytes.Equal(rr.Body.Bytes(), s3Body) {
			t.Errorf("body = %q, want S3 data", rr.Body.Bytes())
		}
	})

	t.Run("404 when local gone and not uploaded", func(t *testing.T) {
		s := newServer(fakeArchive{data: map[string][]byte{"bucket/c/cloud.mp4": s3Body}})
		rr := serveRecording(s, "gone")
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})

	t.Run("404 when S3 disabled", func(t *testing.T) {
		s := newServer(fakeArchive{err: errS3Disabled})
		rr := serveRecording(s, "cloud")
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})

	t.Run("range request served from S3", func(t *testing.T) {
		s := newServer(fakeArchive{data: map[string][]byte{"bucket/c/cloud.mp4": s3Body}})
		r := httptest.NewRequest(http.MethodGet, "/api/recordings/cloud/file", nil)
		r.Header.Set("Range", "bytes=0-4")
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", "cloud")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
		rr := httptest.NewRecorder()
		s.handleRecordingFile(rr, r)
		if rr.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rr.Code)
		}
		if got := rr.Body.Bytes(); !bytes.Equal(got, s3Body[:5]) {
			t.Errorf("range body = %q, want %q", got, s3Body[:5])
		}
	})
}

func mustCreate(t *testing.T, db *database.DB, rec database.Recording) {
	t.Helper()
	if err := db.Recordings.Create(rec); err != nil {
		t.Fatal(err)
	}
}
