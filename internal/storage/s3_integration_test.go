package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// TestS3RoundTripIntegration exercises the real Upload + Open round trip against
// a live S3-compatible endpoint. It runs only when KENKO_S3_ENDPOINT etc. are
// set, so the normal unit suite is unaffected. No credentials live in source.
func TestS3RoundTripIntegration(t *testing.T) {
	endpoint := os.Getenv("KENKO_S3_ENDPOINT")
	access := os.Getenv("KENKO_S3_ACCESS")
	secret := os.Getenv("KENKO_S3_SECRET")
	if endpoint == "" || access == "" || secret == "" {
		t.Skip("set KENKO_S3_ENDPOINT/ACCESS/SECRET to run the S3 integration test")
	}
	bucket := os.Getenv("KENKO_S3_BUCKET")
	useSSL := os.Getenv("KENKO_S3_INSECURE") == ""

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Raw client for discovery / setup / teardown.
	raw, err := minio.New(stripScheme(endpoint), &minio.Options{
		Creds:  credentials.NewStaticV4(access, secret, ""),
		Secure: useSSL,
	})
	if err != nil {
		t.Fatalf("minio.New: %v", err)
	}

	if bucket == "" {
		buckets, err := raw.ListBuckets(ctx)
		if err != nil {
			t.Fatalf("ListBuckets: %v", err)
		}
		for _, b := range buckets {
			t.Logf("found bucket: %s", b.Name)
		}
		if len(buckets) == 0 {
			t.Fatal("no buckets available; set KENKO_S3_BUCKET")
		}
		bucket = buckets[0].Name
	}
	t.Logf("using bucket: %s", bucket)

	cfg := database.S3Config{
		Endpoint:  stripScheme(endpoint),
		Bucket:    bucket,
		AccessKey: access,
		SecretKey: secret,
		UseSSL:    useSSL,
		KeyPrefix: "kenko-itest",
	}
	up, err := NewUploader(cfg)
	if err != nil {
		t.Fatalf("NewUploader: %v", err)
	}

	// Upload a temp file via the code under test.
	body := []byte("kenko-nvr s3 round-trip integration payload \x00\x01\x02\xff")
	relPath := "cam-itest/2026-06-30/clip.mp4"
	tmp, err := os.CreateTemp(t.TempDir(), "clip-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.Write(body); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	key := up.Key(relPath)
	t.Logf("object key: %s", key)
	if err := up.Upload(ctx, tmp.Name(), key); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	t.Cleanup(func() {
		_ = raw.RemoveObject(context.Background(), bucket, key, minio.RemoveObjectOptions{})
	})

	// Read it back via the playback path (Open).
	obj, err := up.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer obj.Close()
	if obj.Size != int64(len(body)) {
		t.Errorf("Open size = %d, want %d", obj.Size, len(body))
	}
	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("round trip mismatch: got %q want %q", got, body)
	}

	// Seek + partial read (proves range/scrub works for playback).
	if _, err := obj.Body.Seek(5, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	part := make([]byte, 4)
	if _, err := io.ReadFull(obj.Body, part); err != nil {
		t.Fatalf("ReadFull after seek: %v", err)
	}
	if !bytes.Equal(part, body[5:9]) {
		t.Errorf("ranged read = %q, want %q", part, body[5:9])
	}
	t.Logf("round trip OK: %d bytes, ranged read OK", obj.Size)
}

func stripScheme(s string) string {
	for _, p := range []string{"https://", "http://"} {
		if len(s) >= len(p) && s[:len(p)] == p {
			return s[len(p):]
		}
	}
	return s
}
