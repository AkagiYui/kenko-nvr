package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.HTTP.Addr != ":8080" {
		t.Errorf("default http addr = %q", c.HTTP.Addr)
	}
	if c.Storage.DBPath == "" || c.Storage.RecordingsDir == "" {
		t.Error("default storage paths must be set")
	}
}

func TestLoadEmptyPathReturnsDefaults(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.Addr != ":8080" {
		t.Errorf("expected defaults, got %q", c.HTTP.Addr)
	}
}

func TestLoadMissingWritesTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.Addr != ":8080" {
		t.Errorf("expected default addr, got %q", c.HTTP.Addr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected template config to be written: %v", err)
	}
}

func TestLoadParsesOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
http:
  addr: "127.0.0.1:9999"
  username: bob
  password: hunter2
rtmp:
  enabled: false
  addr: ":1936"
storage:
  recordings_dir: /data/rec
  db_path: /data/nvr.db
log:
  level: debug
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.Addr != "127.0.0.1:9999" || c.HTTP.Username != "bob" {
		t.Errorf("http override not applied: %+v", c.HTTP)
	}
	if c.RTMP.Enabled {
		t.Error("rtmp.enabled override not applied")
	}
	if c.Storage.RecordingsDir != "/data/rec" {
		t.Errorf("storage override not applied: %q", c.Storage.RecordingsDir)
	}
	if c.Log.Level != "debug" {
		t.Errorf("log override not applied: %q", c.Log.Level)
	}
}

func TestValidateRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("http:\n  addr: \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected validation error for empty http.addr")
	}
}
