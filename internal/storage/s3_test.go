package storage

import (
	"testing"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

func TestUploaderKey(t *testing.T) {
	cases := []struct {
		prefix  string
		relPath string
		want    string
	}{
		{"", "cam/2026/file.mp4", "cam/2026/file.mp4"},
		{"nvr", "cam/file.mp4", "nvr/cam/file.mp4"},
		{"/nvr/", "cam/file.mp4", "nvr/cam/file.mp4"},
		{"backup", "a\\b\\c.mp4", "backup/a/b/c.mp4"},
	}
	for _, c := range cases {
		u, err := NewUploader(database.S3Config{
			Endpoint: "s3.example.com", Bucket: "b", KeyPrefix: c.prefix,
			AccessKey: "a", SecretKey: "s",
		})
		if err != nil {
			t.Fatalf("NewUploader: %v", err)
		}
		if got := u.Key(c.relPath); got != c.want {
			t.Errorf("Key(prefix=%q, %q) = %q, want %q", c.prefix, c.relPath, got, c.want)
		}
	}
}

func TestNewUploaderValidation(t *testing.T) {
	if _, err := NewUploader(database.S3Config{Bucket: "b"}); err == nil {
		t.Error("expected error for missing endpoint")
	}
	if _, err := NewUploader(database.S3Config{Endpoint: "s3.example.com"}); err == nil {
		t.Error("expected error for missing bucket")
	}
}

func TestNewUploaderProxyParse(t *testing.T) {
	_, err := NewUploader(database.S3Config{
		Endpoint: "s3.example.com", Bucket: "b", ProxyURL: "://bad",
	})
	if err == nil {
		t.Error("expected error for invalid proxy url")
	}
}
