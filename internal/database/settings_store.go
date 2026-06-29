package database

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Settings keys.
const (
	keyRetention     = "retention"
	keyS3            = "s3"
	keyRecording     = "recording"
	keyNotifications = "notifications"
	keyHomeAssistant = "homeassistant"
	keyVideoWall     = "videowall"
	keySystem        = "system"
)

// SettingsStore persists JSON-encoded settings blobs keyed by name.
type SettingsStore struct {
	db *sql.DB
}

func (s *SettingsStore) getJSON(key string, dst any) (bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(value), dst); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SettingsStore) setJSON(key string, src any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(data))
	return err
}

// Retention returns the retention policy, or defaults if unset.
func (s *SettingsStore) Retention() (RetentionPolicy, error) {
	p := DefaultRetentionPolicy()
	_, err := s.getJSON(keyRetention, &p)
	return p, err
}

// SetRetention stores the retention policy.
func (s *SettingsStore) SetRetention(p RetentionPolicy) error {
	return s.setJSON(keyRetention, p)
}

// S3 returns the S3 config, or a disabled default if unset.
func (s *SettingsStore) S3() (S3Config, error) {
	var c S3Config
	_, err := s.getJSON(keyS3, &c)
	return c, err
}

// SetS3 stores the S3 config.
func (s *SettingsStore) SetS3(c S3Config) error {
	return s.setJSON(keyS3, c)
}

// Recording returns the recording config, or defaults if unset.
func (s *SettingsStore) Recording() (RecordingConfig, error) {
	c := DefaultRecordingConfig()
	_, err := s.getJSON(keyRecording, &c)
	return c, err
}

// SetRecording stores the recording config.
func (s *SettingsStore) SetRecording(c RecordingConfig) error {
	return s.setJSON(keyRecording, c)
}

// Notifications returns the notification config, or defaults if unset. A config
// stored in the pre-channels format is migrated to a channel list on read.
func (s *SettingsStore) Notifications() (NotificationConfig, error) {
	c := DefaultNotificationConfig()
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, keyNotifications).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, err
	}
	migrateNotificationChannels(&c, []byte(raw))
	return c, nil
}

// migrateNotificationChannels converts a legacy config (single email/webhook/
// mqtt/webPush blocks, no channels) into the channel list, once.
func migrateNotificationChannels(c *NotificationConfig, raw []byte) {
	if len(c.Channels) > 0 {
		return
	}
	var legacy struct {
		Email   EmailConfig   `json:"email"`
		Webhook WebhookConfig `json:"webhook"`
		MQTT    MQTTConfig    `json:"mqtt"`
		WebPush WebPushConfig `json:"webPush"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return
	}
	if legacy.Email.Host != "" {
		c.Channels = append(c.Channels, NotificationChannel{
			ID: "legacy-email", Name: "邮件", Type: ChannelEmail, Enabled: legacy.Email.Enabled, Email: legacy.Email,
		})
	}
	if legacy.Webhook.URL != "" {
		c.Channels = append(c.Channels, NotificationChannel{
			ID: "legacy-webhook", Name: "Webhook", Type: ChannelWebhook, Enabled: legacy.Webhook.Enabled, Webhook: legacy.Webhook,
		})
	}
	if legacy.MQTT.BrokerURL != "" {
		c.Channels = append(c.Channels, NotificationChannel{
			ID: "legacy-mqtt", Name: "MQTT", Type: ChannelMQTT, Enabled: legacy.MQTT.Enabled, MQTT: legacy.MQTT,
		})
	}
	if legacy.WebPush.Enabled {
		c.Channels = append(c.Channels, NotificationChannel{
			ID: "legacy-webpush", Name: "浏览器推送", Type: ChannelWebPush, Enabled: true, Subject: legacy.WebPush.Subject,
		})
	}
}

// SetNotifications stores the notification config.
func (s *SettingsStore) SetNotifications(c NotificationConfig) error {
	return s.setJSON(keyNotifications, c)
}

// System returns the runtime infrastructure config and whether it was present
// (false means it has never been seeded from the YAML bootstrap config).
func (s *SettingsStore) System() (SystemConfig, bool, error) {
	var c SystemConfig
	ok, err := s.getJSON(keySystem, &c)
	return c, ok, err
}

// SetSystem stores the runtime infrastructure config.
func (s *SettingsStore) SetSystem(c SystemConfig) error {
	return s.setJSON(keySystem, c)
}

// HomeAssistant returns the Home Assistant discovery config, or defaults if unset.
func (s *SettingsStore) HomeAssistant() (HAConfig, error) {
	c := DefaultHAConfig()
	_, err := s.getJSON(keyHomeAssistant, &c)
	return c, err
}

// SetHomeAssistant stores the Home Assistant discovery config.
func (s *SettingsStore) SetHomeAssistant(c HAConfig) error {
	return s.setJSON(keyHomeAssistant, c)
}

// VideoWall returns the saved video-wall layouts as an opaque, frontend-defined
// JSON blob (or an empty default if unset).
func (s *SettingsStore) VideoWall() (json.RawMessage, error) {
	var raw json.RawMessage
	ok, err := s.getJSON(keyVideoWall, &raw)
	if err != nil {
		return nil, err
	}
	if !ok || len(raw) == 0 {
		return json.RawMessage(`{"layouts":[]}`), nil
	}
	return raw, nil
}

// SetVideoWall stores the video-wall layouts blob.
func (s *SettingsStore) SetVideoWall(raw json.RawMessage) error {
	return s.setJSON(keyVideoWall, raw)
}
