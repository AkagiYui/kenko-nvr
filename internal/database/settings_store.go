package database

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Settings keys.
const (
	keyRetention = "retention"
	keyS3        = "s3"
	keyRecording = "recording"
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
