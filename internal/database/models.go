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
}

// DefaultRecordingConfig returns sane defaults: 10-minute segments laid out by
// camera and date.
func DefaultRecordingConfig() RecordingConfig {
	return RecordingConfig{
		SegmentSeconds: 600,
		PathTemplate:   "{camera}/{year}-{month}-{day}/{camera}_{year}{month}{day}_{hour}{minute}{second}.mp4",
	}
}
