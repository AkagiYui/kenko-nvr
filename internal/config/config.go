// Package config loads bootstrap configuration from a YAML file.
//
// Only infrastructure-level settings live here (listen addresses, paths,
// admin credentials, log level). Operational settings that the user edits at
// runtime (cameras, retention policy, S3 target) are stored in the database
// instead, so the web UI can change them without a restart.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the root bootstrap configuration.
type Config struct {
	HTTP    HTTPConfig    `yaml:"http"`
	RTMP    RTMPConfig    `yaml:"rtmp"`
	RTSP    RTSPConfig    `yaml:"rtsp"`
	Storage StorageConfig `yaml:"storage"`
	Log     LogConfig     `yaml:"log"`
}

// HTTPConfig configures the management/API/HLS web server.
type HTTPConfig struct {
	Addr     string `yaml:"addr"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// JWTSecret signs session tokens. If empty, a random secret is generated
	// at startup (sessions then do not survive a restart).
	JWTSecret string `yaml:"jwt_secret"`
}

// RTMPConfig configures the embedded RTMP ingest server (for push publishing).
type RTMPConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
}

// RTSPConfig configures defaults for RTSP client pulls.
type RTSPConfig struct {
	// Transport is the preferred lower transport: "automatic", "tcp" or "udp".
	Transport string `yaml:"transport"`
}

// StorageConfig configures local storage locations.
type StorageConfig struct {
	RecordingsDir string `yaml:"recordings_dir"`
	DBPath        string `yaml:"db_path"`
}

// LogConfig configures logging.
type LogConfig struct {
	Level string `yaml:"level"`
}

// Default returns a Config populated with sane defaults.
func Default() Config {
	return Config{
		HTTP: HTTPConfig{
			Addr:     ":8080",
			Username: "admin",
			Password: "admin",
		},
		RTMP: RTMPConfig{
			Enabled: true,
			Addr:    ":1935",
		},
		RTSP: RTSPConfig{
			Transport: "automatic",
		},
		Storage: StorageConfig{
			RecordingsDir: "./data/recordings",
			DBPath:        "./data/nvr.db",
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
