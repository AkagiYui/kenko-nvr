package database

import "time"

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

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Recording is one recorded media file.
type Recording struct {
	ID         string    `json:"id"`
	CameraID   string    `json:"cameraId"`
	Path       string    `json:"path"` // relative to the recordings root
	StartTime  time.Time `json:"startTime"`
	EndTime    time.Time `json:"endTime"`
	DurationMS int64     `json:"durationMs"`
	SizeBytes  int64     `json:"sizeBytes"`
	Complete   bool      `json:"complete"`
	Uploaded   bool      `json:"uploaded"`
	S3Key      string    `json:"s3Key"`
	CreatedAt  time.Time `json:"createdAt"`
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
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"` // never serialized
	Role         Role      `json:"role"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
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
	StartTime time.Time `json:"startTime"`
	EndTime   time.Time `json:"endTime"`
	Score     float64   `json:"score"`
	CreatedAt time.Time `json:"createdAt"`
}

// PushSubscription is a stored Web Push (browser) subscription.
type PushSubscription struct {
	ID        string    `json:"id"`
	Endpoint  string    `json:"endpoint"`
	P256dh    string    `json:"p256dh"`
	Auth      string    `json:"auth"`
	CreatedAt time.Time `json:"createdAt"`
}

// NotificationConfig controls alert delivery. Stored as JSON under the
// "notifications" settings key.
type NotificationConfig struct {
	Enabled bool `json:"enabled"`
	// OnMotion fires a notification when motion starts on a camera.
	OnMotion bool `json:"onMotion"`
	// OnCameraOffline fires when a camera transitions to the error/offline state.
	OnCameraOffline bool `json:"onCameraOffline"`
	// MinIntervalSeconds throttles repeat notifications per camera+kind.
	MinIntervalSeconds int `json:"minIntervalSeconds"`

	Email   EmailConfig   `json:"email"`
	Webhook WebhookConfig `json:"webhook"`
	MQTT    MQTTConfig    `json:"mqtt"`
	WebPush WebPushConfig `json:"webPush"`
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
	Enabled  bool   `json:"enabled"`
	BrokerURL string `json:"brokerURL"` // e.g. tcp://broker:1883
	Username string `json:"username"`
	Password string `json:"password"`
	Topic    string `json:"topic"` // e.g. kenko-nvr/events
	ClientID string `json:"clientID"`
}

// WebPushConfig configures browser Web Push (VAPID). The keypair is generated
// once and stored here so subscriptions remain valid across restarts.
type WebPushConfig struct {
	Enabled    bool   `json:"enabled"`
	Subject    string `json:"subject"`    // mailto: or https URL
	PublicKey  string `json:"publicKey"`  // base64url VAPID public key (sent to the browser)
	PrivateKey string `json:"privateKey"` // base64url VAPID private key (server-only)
}

// DefaultNotificationConfig returns a disabled default notification config.
func DefaultNotificationConfig() NotificationConfig {
	return NotificationConfig{
		Enabled:            false,
		OnMotion:           true,
		OnCameraOffline:    true,
		MinIntervalSeconds: 60,
		Email:              EmailConfig{Port: 587, UseTLS: true},
		MQTT:               MQTTConfig{Topic: "kenko-nvr/events", ClientID: "kenko-nvr"},
		WebPush:            WebPushConfig{Subject: "mailto:admin@example.com"},
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
