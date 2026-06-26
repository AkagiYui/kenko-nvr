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
	HTTP       HTTPConfig       `yaml:"http"`
	RTMP       RTMPConfig       `yaml:"rtmp"`
	RTSP       RTSPConfig       `yaml:"rtsp"`
	RTSPServer RTSPServerConfig `yaml:"rtsp_server"`
	WebRTC     WebRTCConfig     `yaml:"webrtc"`
	Storage    StorageConfig    `yaml:"storage"`
	Transcode  TranscodeConfig  `yaml:"transcode"`
	Log        LogConfig        `yaml:"log"`
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

// RTSPServerConfig configures the RTSP re-publishing server, which lets external
// clients pull rtsp://host:<addr>/<cameraID>.
type RTSPServerConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
}

// WebRTCConfig configures low-latency WebRTC live view.
type WebRTCConfig struct {
	// Enabled turns the WHEP WebRTC endpoint on.
	Enabled bool `yaml:"enabled"`
	// STUNServers are ICE STUN servers used for NAT traversal across networks
	// (a LAN deployment works without any). e.g. ["stun:stun.l.google.com:19302"].
	STUNServers []string `yaml:"stun_servers"`
}

// StorageConfig configures local storage locations.
type StorageConfig struct {
	RecordingsDir string `yaml:"recordings_dir"`
	DBPath        string `yaml:"db_path"`
}

// TranscodeConfig configures on-demand live transcoding (used to make non-H.264
// cameras playable in the browser). Recording always archives the original
// stream untouched; this affects the live view path only.
type TranscodeConfig struct {
	// HWAccel selects the FFmpeg encoder for live transcode: "auto" probes the
	// machine and picks the best *working* hardware encoder (else software);
	// "none"/"software" forces software libx264; any other value names a specific
	// encoder (e.g. "h264_videotoolbox", "h264_nvenc", "h264_vaapi"). The encoder
	// is verified at startup, so a misconfigured name simply falls back.
	HWAccel string `yaml:"hwaccel"`
	// LiveBitrateKbps is the target bitrate (kbit/s) for transcoded live video.
	LiveBitrateKbps int `yaml:"live_bitrate_kbps"`
	// LiveGOP is the keyframe interval (frames) for transcoded live video; a
	// shorter interval lowers join latency. At 25 fps, 50 ≈ 2 s.
	LiveGOP int `yaml:"live_gop"`
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
		RTSPServer: RTSPServerConfig{
			Enabled: true,
			Addr:    ":8554",
		},
		WebRTC: WebRTCConfig{
			Enabled: true,
		},
		Storage: StorageConfig{
			RecordingsDir: "./data/recordings",
			DBPath:        "./data/nvr.db",
		},
		Transcode: TranscodeConfig{
			HWAccel:         "auto",
			LiveBitrateKbps: 2500,
			LiveGOP:         50,
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
