// Package config loads bootstrap configuration from a YAML file.
//
// Only settings needed before the database is available live here: the HTTP
// listen address and JWT secret, storage paths, and the log level. Everything
// else — cameras, retention, S3, notifications, live transcoding, and the
// RTMP/RTSP/RTSP-server/WebRTC/GB28181 network services — is stored in the
// database and edited at runtime in the web UI. The web login account defaults
// to admin/admin and is managed under Users.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the root bootstrap configuration. Only settings that must be known
// before the database is usable live here; everything operational (cameras,
// retention, S3, notifications, and the RTMP/RTSP/RTSP-server/WebRTC/GB28181
// network services) is stored in the database and edited at runtime in the web
// UI. The web login account is seeded as admin/admin and managed under Users.
type Config struct {
	HTTP    HTTPConfig    `yaml:"http"`
	Storage StorageConfig `yaml:"storage"`
	Log     LogConfig     `yaml:"log"`
}

// HTTPConfig configures the management/API/HLS web server.
type HTTPConfig struct {
	Addr string `yaml:"addr"`
	// JWTSecret signs session tokens. If empty, a random secret is generated
	// at startup (sessions then do not survive a restart).
	JWTSecret string `yaml:"jwt_secret"`
}

// StorageConfig configures local storage locations.
type StorageConfig struct {
	RecordingsDir string `yaml:"recordings_dir"`
	DBPath        string `yaml:"db_path"`
	// FacesDir holds cropped face thumbnails (optionally KNV1-encrypted). Defaults
	// to a "faces" sibling of the database when empty.
	FacesDir string `yaml:"faces_dir"`
}

// LogConfig configures logging.
type LogConfig struct {
	Level string `yaml:"level"`
}

// Default returns a Config populated with sane defaults.
func Default() Config {
	return Config{
		HTTP: HTTPConfig{
			Addr: ":8080",
		},
		Storage: StorageConfig{
			RecordingsDir: "./data/recordings",
			DBPath:        "./data/nvr.db",
			FacesDir:      "./data/faces",
		},
		Log: LogConfig{
			Level: "info",
		},
	}
}

// Load reads configuration from path. If path is empty or the file does not
// exist, defaults are returned (and, when path is non-empty, written to disk so
// the user has a template to edit).
func Load(path string) (Config, error) {
	cfg := Default()

	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Materialise a default config so the operator can edit it.
			if werr := writeDefault(path, cfg); werr != nil {
				return cfg, fmt.Errorf("writing default config: %w", werr)
			}
			return cfg, nil
		}
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.HTTP.Addr == "" {
		return errors.New("http.addr must not be empty")
	}
	if c.Storage.RecordingsDir == "" {
		return errors.New("storage.recordings_dir must not be empty")
	}
	if c.Storage.DBPath == "" {
		return errors.New("storage.db_path must not be empty")
	}
	return nil
}

func writeDefault(path string, cfg Config) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
