package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

func newSettingsServer(t *testing.T) *Server {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{db: db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func putS3(t *testing.T, s *Server, cfg database.S3Config) database.S3Config {
	t.Helper()
	body, _ := json.Marshal(cfg)
	r := httptest.NewRequest(http.MethodPut, "/api/settings/s3", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleSetS3(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT s3 status = %d: %s", rr.Code, rr.Body.String())
	}
	var out database.S3Config
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestS3EncryptionSettings(t *testing.T) {
	s := newSettingsServer(t)

	// Enable encryption with a passphrase: the server mints a salt and never
	// echoes the passphrase back.
	resp := putS3(t, s, database.S3Config{
		Enabled: true, Endpoint: "s3.example.com", Bucket: "b",
		EncryptionEnabled: true, EncryptionKey: "hunter2",
	})
	if resp.EncryptionKey != "" {
		t.Error("PUT response leaked the encryption passphrase")
	}

	stored, _ := s.db.Settings.S3()
	if stored.EncryptionKey != "hunter2" {
		t.Errorf("passphrase not stored: %q", stored.EncryptionKey)
	}
	if stored.EncryptionSalt == "" {
		t.Error("salt not generated when encryption enabled")
	}
	salt := stored.EncryptionSalt

	// GET masks the passphrase but keeps the (non-secret) enabled flag.
	rr := httptest.NewRecorder()
	s.handleGetS3(rr, httptest.NewRequest(http.MethodGet, "/api/settings/s3", nil))
	var got database.S3Config
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.EncryptionKey != "" {
		t.Error("GET leaked the encryption passphrase")
	}
	if !got.EncryptionEnabled {
		t.Error("GET should report encryption enabled")
	}

	// A subsequent save with a blank passphrase preserves the stored one and the
	// salt stays stable (changing it would orphan already-encrypted objects).
	putS3(t, s, database.S3Config{
		Enabled: true, Endpoint: "s3.example.com", Bucket: "b",
		EncryptionEnabled: true, EncryptionKey: "",
	})
	stored2, _ := s.db.Settings.S3()
	if stored2.EncryptionKey != "hunter2" {
		t.Errorf("blank passphrase did not preserve existing: %q", stored2.EncryptionKey)
	}
	if stored2.EncryptionSalt != salt {
		t.Error("salt changed across saves")
	}
}
