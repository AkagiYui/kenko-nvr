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
	GB28181    GB28181Config    `yaml:"gb28181"`
	Storage    StorageConfig    `yaml:"storage"`
	Transcode  TranscodeConfig  `yaml:"transcode"`
	Log        LogConfig        `yaml:"log"`
}

// GB28181Config configures the embedded GB/T 28181 SIP platform. When enabled,
// IP cameras / NVRs configured to register to this server appear as devices whose
// channels can be added as cameras (source type "gb28181").
type GB28181Config struct {
	// Enabled turns the SIP server on.
	Enabled bool `yaml:"enabled"`
	// SIPAddr is the UDP listen address for SIP signalling (e.g. ":5060").
	SIPAddr string `yaml:"sip_addr"`
	// ServerID is the 20-digit platform/server SIP ID devices register to.
	ServerID string `yaml:"server_id"`
	// Domain is the SIP domain / realm (typically the first 10 digits of ServerID).
	Domain string `yaml:"domain"`
	// Password is the shared device registration password ("" disables auth).
	Password string `yaml:"password"`
	// MediaIP is the IP advertised to devices for media; auto-detected if empty.
	MediaIP string `yaml:"media_ip"`
	// MediaPortMin/Max bound the UDP ports used to receive RTP/PS media.
	MediaPortMin int `yaml:"media_port_min"`
	MediaPortMax int `yaml:"media_port_max"`
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
		GB28181: GB28181Config{
			Enabled:      false,
			SIPAddr:      ":5060",
			ServerID:     "34020000002000000001",
			Domain:       "3402000000",
			Password:     "",
			MediaPortMin: 30000,
			MediaPortMax: 30500,
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
