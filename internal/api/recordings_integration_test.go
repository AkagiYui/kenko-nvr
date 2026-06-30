package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/AkagiYui/kenko-nvr/internal/config"
	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/storage"
)

// TestRecordingS3PlaybackIntegration is the full end-to-end: upload a clip to a
// live bucket, mark the row uploaded+local_removed, then GET the file handler
// with no local file and assert it streams the bytes back from S3 (full and
// ranged). Runs only when KENKO_S3_ENDPOINT/ACCESS/SECRET are set.
func TestRecordingS3PlaybackIntegration(t *testing.T) {
	endpoint := os.Getenv("KENKO_S3_ENDPOINT")
	access := os.Getenv("KENKO_S3_ACCESS")
	secret := os.Getenv("KENKO_S3_SECRET")
	if endpoint == "" || access == "" || secret == "" {
		t.Skip("set KENKO_S3_ENDPOINT/ACCESS/SECRET to run the S3 playback integration test")
	}
	host := endpoint
	for _, p := range []string{"https://", "http://"} {
		host = strings.TrimPrefix(host, p)
	}
	useSSL := os.Getenv("KENKO_S3_INSECURE") == ""

	raw, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(access, secret, ""),
		Secure: useSSL,
	})
	if err != nil {
		t.Fatal(err)
	}

	bucket := os.Getenv("KENKO_S3_BUCKET")
	if bucket == "" {
		buckets, err := raw.ListBuckets(context.Background())
		if err != nil {
			t.Fatalf("ListBuckets: %v", err)
		}
		if len(buckets) == 0 {
			t.Skip("no buckets available; set KENKO_S3_BUCKET")
		}
		bucket = buckets[0].Name
	}

	cfg := database.S3Config{
		Enabled:   true,
		Endpoint:  host,
		Bucket:    bucket,
		AccessKey: access,
		SecretKey: secret,
		UseSSL:    useSSL,
		KeyPrefix: "kenko-itest",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Upload a clip to S3 (no local file kept).
	up, err := storage.NewUploader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("KENKO-CLIP-"), 4096) // ~44 KB
	tmp := filepath.Join(t.TempDir(), "src.mp4")
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		t.Fatal(err)
	}
	relPath := "cam-itest/2026-06-30/playback.mp4"
	key := up.Key(relPath)
	if err := up.Upload(ctx, tmp, key); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	t.Cleanup(func() {
		_ = raw.RemoveObject(context.Background(), bucket, key, minio.RemoveObjectOptions{})
	})

	// Server backed by a real DB whose S3 settings point at the live bucket; the
	// recordings dir is empty, so playback must fall back to S3.
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Settings.SetS3(cfg); err != nil {
		t.Fatal(err)
	}
	if err := db.Cameras.Create(database.Camera{ID: "c", Name: "c", SourceType: database.SourceRTSP}); err != nil {
		t.Fatal(err)
	}
	if err := db.Recordings.Create(database.Recording{
		ID: "r1", CameraID: "c", Path: relPath, Complete: true,
		Uploaded: true, S3Key: key, LocalRemoved: true,
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		cfg:     config.Config{Storage: config.StorageConfig{RecordingsDir: t.TempDir()}},
		db:      db,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		archive: s3Archive{settings: db.Settings},
	}

	get := func(rangeHdr string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/recordings/r1/file", nil)
		if rangeHdr != "" {
			r.Header.Set("Range", rangeHdr)
		}
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", "r1")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
		rr := httptest.NewRecorder()
		s.handleRecordingFile(rr, r)
		return rr
	}

	// Full playback.
	rr := get("")
	if rr.Code != http.StatusOK {
		t.Fatalf("full GET status = %d", rr.Code)
	}
	if !bytes.Equal(rr.Body.Bytes(), body) {
		t.Errorf("full body mismatch: got %d bytes, want %d", rr.Body.Len(), len(body))
	}
	if ct := rr.Header().Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("Content-Type = %q", ct)
	}

	// Ranged playback (scrubbing).
	rr = get("bytes=10-19")
	if rr.Code != http.StatusPartialContent {
		t.Fatalf("ranged GET status = %d, want 206", rr.Code)
	}
	if got := rr.Body.Bytes(); !bytes.Equal(got, body[10:20]) {
		t.Errorf("ranged body = %q, want %q", got, body[10:20])
	}
	t.Logf("end-to-end S3 playback OK: %d bytes full, 206 range OK", len(body))
}
