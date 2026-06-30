package database

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// EpochMS is a time.Time that crosses the JSON API as an absolute Unix-
// millisecond timestamp (a number, or null when unset) instead of a
// timezone-bearing RFC3339 string. Sending an absolute instant — never a local
// wall-clock string — lets the frontend render every time in the viewer's own
// timezone (see the web app's fmtTime); the server's timezone never leaks onto
// the wire. It is stored in SQLite as the same millisecond integer (timeToMS).
type EpochMS struct{ time.Time }

// MS wraps a time.Time so it serializes as epoch milliseconds.
func MS(t time.Time) EpochMS { return EpochMS{t} }

// MarshalJSON renders the instant as epoch milliseconds, or null when unset.
func (e EpochMS) MarshalJSON() ([]byte, error) {
	if e.Time.IsZero() {
		return []byte("null"), nil
	}
	return []byte(strconv.FormatInt(e.Time.UnixMilli(), 10)), nil
}

// UnmarshalJSON accepts epoch milliseconds (number) or null; for robustness
// against older clients it also parses an RFC3339 string.
func (e *EpochMS) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	switch {
	case s == "" || s == "null":
		e.Time = time.Time{}
	case s[0] == '"':
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		if str == "" {
			e.Time = time.Time{}
			return nil
		}
		t, err := time.Parse(time.RFC3339, str)
		if err != nil {
			return err
		}
		e.Time = t
	default:
		ms, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		e.Time = time.UnixMilli(ms)
	}
	return nil
}

// SourceType enumerates how a camera's media is obtained.
type SourceType string

const (
	// SourceRTSP pulls media from an RTSP URL (the common IP-camera case).
	SourceRTSP SourceType = "rtsp"
	// SourceRTMP pulls media from an RTMP URL, or receives an RTMP push whose
	// stream key equals the camera ID.
	SourceRTMP SourceType = "rtmp"
	// SourceONVIF resolves the RTSP stream URI from an ONVIF device at connect
	// time, then pulls over RTSP. Implies ONVIF control (PTZ) as well.
	SourceONVIF SourceType = "onvif"
	// SourceGB28181 binds the camera to a channel of a GB/T 28181 device that has
	// registered to the embedded SIP platform. The stream is obtained by INVITE
	// (RTP/MPEG-PS) rather than by pulling a URL.
	SourceGB28181 SourceType = "gb28181"
)

// Camera is a configured media source.
type Camera struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	SourceType SourceType `json:"sourceType"`
	URL        string     `json:"url"`
	Username   string     `json:"username"`
	Password   string     `json:"password"`
	Transport  string     `json:"transport"` // rtsp transport override: "", "tcp", "udp"
	Record     bool       `json:"record"`

	// Motion detection / event-triggered recording.
	MotionEnabled bool `json:"motionEnabled"`
	// RecordMode is "continuous" (record whenever live, the default) or "motion"
	// (only record while motion is detected, plus a short post-roll). "motion"
	// implies MotionEnabled.
	RecordMode string `json:"recordMode"`
	// MotionSensitivity is 0–100; higher detects smaller/subtler movement.
	MotionSensitivity int `json:"motionSensitivity"`

	// ONVIF control (optional, independent of the media source).
	OnvifEnabled  bool   `json:"onvifEnabled"`
	OnvifXAddr    string `json:"onvifXAddr"`
	OnvifUsername string `json:"onvifUsername"`
	OnvifPassword string `json:"onvifPassword"`
	OnvifProfile  string `json:"onvifProfile"`

	// GB28181 binding (used when SourceType == SourceGB28181). DeviceID is the
	// registered device's 20-digit national-standard ID; ChannelID is the
	// channel's ID (defaults to DeviceID for single-channel devices).
	GB28181DeviceID  string `json:"gb28181DeviceId"`
	GB28181ChannelID string `json:"gb28181ChannelId"`

	CreatedAt EpochMS `json:"createdAt"`
	UpdatedAt EpochMS `json:"updatedAt"`
}

// Recording is one recorded media file.
type Recording struct {
	ID         string  `json:"id"`
	CameraID   string  `json:"cameraId"`
	Path       string  `json:"path"` // relative to the recordings root
	StartTime  EpochMS `json:"startTime"`
	EndTime    EpochMS `json:"endTime"`
	DurationMS int64   `json:"durationMs"`
	SizeBytes  int64   `json:"sizeBytes"`
	Complete   bool    `json:"complete"`
	Uploaded   bool    `json:"uploaded"`
	S3Key      string  `json:"s3Key"`
	// LocalRemoved is true when the local file has been deleted but the recording
	// is preserved on S3 (Uploaded && S3Key set). Such a clip stays listed and is
	// played by streaming it back from S3 through the NVR; it no longer counts
	// toward local-disk usage.
	LocalRemoved bool `json:"localRemoved"`
	// Encrypted is true when the S3 object is client-side encrypted and must be
	// decrypted on download/playback. The local file (if present) is plaintext.
	Encrypted bool    `json:"encrypted"`
	CreatedAt EpochMS `json:"createdAt"`
}

// Role enumerates a user's permission level.
type Role string

const (
	// RoleAdmin can do everything, including managing users and settings.
	RoleAdmin Role = "admin"
	// RoleOperator can manage cameras and control PTZ/talk, but not users or
	// system settings.
	RoleOperator Role = "operator"
	// RoleViewer can only view live/recordings.
	RoleViewer Role = "viewer"
)

// ValidRole reports whether r is a known role.
func ValidRole(r Role) bool {
	switch r {
	case RoleAdmin, RoleOperator, RoleViewer:
		return true
	default:
		return false
	}
}

// User is a login account.
type User struct {
	ID           string  `json:"id"`
	Username     string  `json:"username"`
	PasswordHash string  `json:"-"` // never serialized
	Role         Role    `json:"role"`
	CreatedAt    EpochMS `json:"createdAt"`
	UpdatedAt    EpochMS `json:"updatedAt"`
}

// EventType enumerates the kinds of detected events.
type EventType string

const (
	// EventMotion is a motion-detection event.
	EventMotion EventType = "motion"
)

// Event is a detected occurrence (currently motion) on a camera, used by the
// timeline UI and notifications.
type Event struct {
	ID        string    `json:"id"`
	CameraID  string    `json:"cameraId"`
	Type      EventType `json:"type"`
	StartTime EpochMS   `json:"startTime"`
	EndTime   EpochMS   `json:"endTime"`
	Score     float64   `json:"score"`
	CreatedAt EpochMS   `json:"createdAt"`
}

// PushSubscription is a stored Web Push (browser) subscription.
type PushSubscription struct {
	ID        string  `json:"id"`
	Endpoint  string  `json:"endpoint"`
	P256dh    string  `json:"p256dh"`
	Auth      string  `json:"auth"`
	CreatedAt EpochMS `json:"createdAt"`
}

// Notification event kinds a channel can subscribe to.
const (
	NotifyKindMotion  = "motion"
	NotifyKindOffline = "offline"
)

// ChannelType identifies a notification channel's delivery method.
type ChannelType string

const (
	ChannelEmail   ChannelType = "email"
	ChannelWebhook ChannelType = "webhook"
	ChannelMQTT    ChannelType = "mqtt"
	ChannelWebPush ChannelType = "webpush"
)

// NotificationConfig controls alert delivery. Stored as JSON under the
// "notifications" settings key. Delivery fans out to an arbitrary list of
// Channels; the top-level OnMotion/OnCameraOffline flags are the default set of
// event kinds a channel delivers when it does not pick its own.
type NotificationConfig struct {
	Enabled bool `json:"enabled"`
	// OnMotion fires a notification when motion starts on a camera.
	OnMotion bool `json:"onMotion"`
	// OnCameraOffline fires when a camera transitions to the error/offline state.
	OnCameraOffline bool `json:"onCameraOffline"`
	// MinIntervalSeconds throttles repeat notifications per camera+kind.
	MinIntervalSeconds int `json:"minIntervalSeconds"`

	// Channels is the user-defined list of delivery channels.
	Channels []NotificationChannel `json:"channels"`

	// WebPush holds the global VAPID keypair shared by every webpush channel and
	// all browser subscriptions; individual channels only carry a Subject.
	WebPush WebPushConfig `json:"webPush"`
}

// NotificationChannel is one delivery target: a method (Type) plus its config
// and the event kinds it handles.
type NotificationChannel struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Type    ChannelType `json:"type"`
	Enabled bool        `json:"enabled"`
	// Events restricts which notification kinds this channel delivers. Empty
	// means follow the global defaults (OnMotion / OnCameraOffline).
	Events []string `json:"events"`

	// Only the field matching Type is used.
	Email   EmailConfig   `json:"email"`
	Webhook WebhookConfig `json:"webhook"`
	MQTT    MQTTConfig    `json:"mqtt"`
	// Subject is the webpush per-channel VAPID subject (mailto: or https URL).
	Subject string `json:"subject"`
}

// WantsKind reports whether this channel should deliver the given event kind,
// given the global defaults to fall back on when the channel selects nothing.
func (ch NotificationChannel) WantsKind(kind string, cfg NotificationConfig) bool {
	if len(ch.Events) > 0 {
		for _, e := range ch.Events {
			if e == kind {
				return true
			}
		}
		return false
	}
	switch kind {
	case NotifyKindMotion:
		return cfg.OnMotion
	case NotifyKindOffline:
		return cfg.OnCameraOffline
	default:
		return true // synthetic kinds (e.g. "test") always pass the default gate
	}
}

// FirstMQTT returns the config of the first MQTT channel (preferring an enabled,
// configured one) and whether one exists. Home Assistant discovery shares this
// broker, so it reads the MQTT settings through here.
func (c NotificationConfig) FirstMQTT() (MQTTConfig, bool) {
	var fallback *MQTTConfig
	for i := range c.Channels {
		ch := &c.Channels[i]
		if ch.Type != ChannelMQTT || ch.MQTT.BrokerURL == "" {
			continue
		}
		if ch.Enabled {
			return ch.MQTT, true
		}
		if fallback == nil {
			fallback = &ch.MQTT
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return MQTTConfig{}, false
}

// EmailConfig configures SMTP email alerts.
type EmailConfig struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	To       string `json:"to"` // comma-separated recipients
	UseTLS   bool   `json:"useTLS"`
}

// WebhookConfig configures an HTTP webhook alert.
type WebhookConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
}

// MQTTConfig configures publishing alerts to an MQTT broker.
type MQTTConfig struct {
	Enabled   bool   `json:"enabled"`
	BrokerURL string `json:"brokerURL"` // e.g. tcp://broker:1883
	Username  string `json:"username"`
	Password  string `json:"password"`
	Topic     string `json:"topic"` // e.g. kenko-nvr/events
	ClientID  string `json:"clientID"`
}

// WebPushConfig configures browser Web Push (VAPID). The keypair is generated
// once and stored here so subscriptions remain valid across restarts.
type WebPushConfig struct {
	Enabled    bool   `json:"enabled"`
	Subject    string `json:"subject"`    // mailto: or https URL
	PublicKey  string `json:"publicKey"`  // base64url VAPID public key (sent to the browser)
	PrivateKey string `json:"privateKey"` // base64url VAPID private key (server-only)
}

// DefaultNotificationConfig returns a disabled default notification config with
// no channels.
func DefaultNotificationConfig() NotificationConfig {
	return NotificationConfig{
		Enabled:            false,
		OnMotion:           true,
		OnCameraOffline:    true,
		MinIntervalSeconds: 60,
		WebPush:            WebPushConfig{Subject: "mailto:admin@example.com"},
	}
}

// SystemConfig holds infrastructure settings that used to live only in the YAML
// bootstrap file but are now editable at runtime (stored under the "system"
// settings key, seeded from the YAML config on first run). Changing them via the
// web UI restarts the affected servers without a process restart.
type SystemConfig struct {
	RTMP       RTMPSettings       `json:"rtmp"`
	RTSP       RTSPSettings       `json:"rtsp"`
	RTSPServer RTSPServerSettings `json:"rtspServer"`
	WebRTC     WebRTCSettings     `json:"webrtc"`
	GB28181    GB28181Settings    `json:"gb28181"`
	Transcode  TranscodeSettings  `json:"transcode"`
}

// TranscodeSettings configures on-demand live transcoding (making non-H.264
// cameras playable in the browser). Recording always archives the original
// stream untouched; these affect the live-view path only.
type TranscodeSettings struct {
	// HWAccel selects the FFmpeg encoder: "auto" probes the machine and picks the
	// best working hardware encoder (else software); "none"/"software" forces
	// libx264; any other value names a specific encoder (e.g. h264_videotoolbox,
	// h264_nvenc, h264_qsv, h264_vaapi), verified before use with software fallback.
	HWAccel string `json:"hwaccel"`
	// LiveBitrateKbps is the target bitrate (kbit/s) for transcoded live video.
	LiveBitrateKbps int `json:"liveBitrateKbps"`
	// LiveGOP is the keyframe interval (frames); shorter lowers join latency.
	LiveGOP int `json:"liveGop"`
}

// DefaultSystemConfig returns the built-in defaults for the runtime
// infrastructure config, used to seed the database on first run and as a
// fallback. These values are hardcoded (not configurable via the YAML file).
func DefaultSystemConfig() SystemConfig {
	return SystemConfig{
		RTMP:       RTMPSettings{Enabled: true, Addr: ":1935"},
		RTSP:       RTSPSettings{Transport: "automatic"},
		RTSPServer: RTSPServerSettings{Enabled: true, Addr: ":8554"},
		WebRTC:     WebRTCSettings{Enabled: true},
		GB28181: GB28181Settings{
			Enabled:      false,
			SIPAddr:      ":5060",
			ServerID:     "34020000002000000001",
			Domain:       "3402000000",
			MediaPortMin: 30000,
			MediaPortMax: 30500,
		},
		Transcode: TranscodeSettings{HWAccel: "auto", LiveBitrateKbps: 2500, LiveGOP: 50},
	}
}

// RTMPSettings configures the embedded RTMP ingest server (push publishing).
type RTMPSettings struct {
	Enabled bool   `json:"enabled"`
	Addr    string `json:"addr"`
}

// RTSPSettings configures defaults for RTSP client pulls.
type RTSPSettings struct {
	Transport string `json:"transport"` // "automatic" | "tcp" | "udp"
}

// RTSPServerSettings configures the RTSP re-publishing server.
type RTSPServerSettings struct {
	Enabled bool   `json:"enabled"`
	Addr    string `json:"addr"`
}

// WebRTCSettings configures low-latency WebRTC live view.
type WebRTCSettings struct {
	Enabled     bool     `json:"enabled"`
	STUNServers []string `json:"stunServers"`
}

// GB28181Settings configures the embedded GB/T 28181 SIP platform.
type GB28181Settings struct {
	Enabled      bool   `json:"enabled"`
	SIPAddr      string `json:"sipAddr"`
	ServerID     string `json:"serverId"`
	Domain       string `json:"domain"`
	Password     string `json:"password"`
	MediaIP      string `json:"mediaIp"`
	MediaPortMin int    `json:"mediaPortMin"`
	MediaPortMax int    `json:"mediaPortMax"`
}

// HAConfig configures Home Assistant MQTT discovery. It publishes each camera as
// an HA device (a motion binary_sensor plus an online/connectivity binary_sensor)
// to the same MQTT broker configured for notifications. Stored as JSON under the
// "homeassistant" settings key.
type HAConfig struct {
	// Enabled turns Home Assistant discovery on. Requires the notifications MQTT
	// broker to be configured (the same connection is reused).
	Enabled bool `json:"enabled"`
	// DiscoveryPrefix is HA's MQTT discovery prefix (default "homeassistant").
	DiscoveryPrefix string `json:"discoveryPrefix"`
	// BaseTopic prefixes the per-camera state topics (default "kenko-nvr").
	BaseTopic string `json:"baseTopic"`
}

// DefaultHAConfig returns a disabled default Home Assistant config.
func DefaultHAConfig() HAConfig {
	return HAConfig{
		Enabled:         false,
		DiscoveryPrefix: "homeassistant",
		BaseTopic:       "kenko-nvr",
	}
}

// RetentionPolicy controls rolling deletion of recordings. Stored as JSON under
// the "retention" settings key.
type RetentionPolicy struct {
	// Enabled turns the retention worker on.
	Enabled bool `json:"enabled"`
	// MaxAgeDays deletes recordings older than this many days (0 = no age limit).
	MaxAgeDays int `json:"maxAgeDays"`
	// MaxTotalSizeGB deletes the oldest recordings once total usage exceeds this
	// size (0 = no size limit).
	MaxTotalSizeGB float64 `json:"maxTotalSizeGB"`
	// MinFreeSpaceGB deletes the oldest recordings while free disk space on the
	// recordings volume is below this threshold (0 = no free-space floor).
	MinFreeSpaceGB float64 `json:"minFreeSpaceGB"`
	// DeleteAfterUpload, when true, only deletes recordings that have already
	// been uploaded to S3 (when S3 is enabled).
	DeleteAfterUpload bool `json:"deleteAfterUpload"`
}

// DefaultRetentionPolicy returns a conservative default policy.
func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		Enabled:        false,
		MaxAgeDays:     30,
		MaxTotalSizeGB: 0,
		MinFreeSpaceGB: 5,
	}
}

// S3Config controls upload of completed recordings to an S3-compatible bucket.
// Stored as JSON under the "s3" settings key.
type S3Config struct {
	Enabled   bool   `json:"enabled"`
	Endpoint  string `json:"endpoint"` // host[:port], e.g. s3.amazonaws.com
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	UseSSL    bool   `json:"useSSL"`
	// KeyPrefix is prepended to object keys.
	KeyPrefix string `json:"keyPrefix"`
	// ProxyURL routes S3 traffic through an HTTP/HTTPS proxy (e.g.
	// http://user:pass@proxy:3128). Empty means a direct connection.
	ProxyURL string `json:"proxyURL"`
	// DeleteLocalAfterUpload removes the local file once uploaded.
	DeleteLocalAfterUpload bool `json:"deleteLocalAfterUpload"`

	// EncryptionEnabled turns on client-side encryption: recordings are
	// encrypted (AES-256-CTR) before upload and decrypted transparently on
	// download/playback, so the storage provider only ever holds ciphertext.
	EncryptionEnabled bool `json:"encryptionEnabled"`
	// EncryptionKey is the passphrase the AES key is derived from (Argon2id).
	// It is a secret: masked on read and preserved when submitted blank, like
	// SecretKey. Changing it makes already-encrypted recordings unreadable.
	EncryptionKey string `json:"encryptionKey"`
	// EncryptionSalt is the base64 per-install KDF salt, generated by the server
	// when encryption is first enabled. Not secret, but must be stable.
	EncryptionSalt string `json:"encryptionSalt"`
}

// RecordingConfig controls how the recorder splits files. Stored as JSON under
// the "recording" settings key.
type RecordingConfig struct {
	// SegmentDuration is the target length of each recording file.
	SegmentSeconds int `json:"segmentSeconds"`
	// PathTemplate names files using strftime-like tokens and %-placeholders.
	// See recording/naming.go for the supported tokens.
	PathTemplate string `json:"pathTemplate"`
	// AlignToClock cuts segments on wall-clock boundaries (e.g. 10-minute
	// segments start at :00, :10, :20) rather than SegmentSeconds after the
	// previous cut.
	AlignToClock bool `json:"alignToClock"`

	// Transcode re-encodes recordings with FFmpeg instead of copying the source
	// stream. Requires the `ffmpeg` binary on PATH; falls back to copy if it is
	// missing. Costs CPU but lets the recording codec be chosen (e.g. H.264 for
	// universal playback) and gives exact, keyframe-accurate clock-aligned cuts.
	Transcode bool `json:"transcode"`
	// TranscodeVideoCodec is the target video codec when Transcode is on:
	// "h264" (default) or "hevc".
	TranscodeVideoCodec string `json:"transcodeVideoCodec"`
	// TranscodeCRF is the libx264/libx265 quality factor (0-51, lower = better;
	// 23 is a good default).
	TranscodeCRF int `json:"transcodeCRF"`
	// TranscodePreset is the libx264/libx265 speed preset
	// (ultrafast … veryslow).
	TranscodePreset string `json:"transcodePreset"`
}

// DefaultRecordingConfig returns sane defaults: 10-minute clock-aligned
// segments laid out by camera and date, stream-copied (no transcode).
func DefaultRecordingConfig() RecordingConfig {
	return RecordingConfig{
		SegmentSeconds:      600,
		PathTemplate:        "{camera}/{year}-{month}-{day}/{camera}_{year}{month}{day}_{hour}{minute}{second}.mp4",
		AlignToClock:        true,
		Transcode:           false,
		TranscodeVideoCodec: "h264",
		TranscodeCRF:        23,
		TranscodePreset:     "fast",
	}
}
