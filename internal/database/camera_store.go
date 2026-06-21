package database

import (
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// CameraStore persists cameras.
type CameraStore struct {
	db *sql.DB
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func timeToMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// List returns all cameras ordered by name.
func (s *CameraStore) List() ([]Camera, error) {
	rows, err := s.db.Query(`SELECT id, name, enabled, source_type, url, username, password,
		transport, record, onvif_enabled, onvif_xaddr, onvif_username, onvif_password,
		onvif_profile, created_at, updated_at FROM cameras ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Camera
	for rows.Next() {
		c, err := scanCamera(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one camera by ID.
func (s *CameraStore) Get(id string) (Camera, error) {
	row := s.db.QueryRow(`SELECT id, name, enabled, source_type, url, username, password,
		transport, record, onvif_enabled, onvif_xaddr, onvif_username, onvif_password,
		onvif_profile, created_at, updated_at FROM cameras WHERE id = ?`, id)
	c, err := scanCamera(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Camera{}, ErrNotFound
	}
	return c, err
}

// Create inserts a new camera.
func (s *CameraStore) Create(c Camera) error {
	now := time.Now()
	c.CreatedAt, c.UpdatedAt = now, now
	_, err := s.db.Exec(`INSERT INTO cameras (id, name, enabled, source_type, url, username,
		password, transport, record, onvif_enabled, onvif_xaddr, onvif_username, onvif_password,
		onvif_profile, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, boolToInt(c.Enabled), string(c.SourceType), c.URL, c.Username, c.Password,
		c.Transport, boolToInt(c.Record), boolToInt(c.OnvifEnabled), c.OnvifXAddr, c.OnvifUsername,
		c.OnvifPassword, c.OnvifProfile, timeToMS(c.CreatedAt), timeToMS(c.UpdatedAt))
	return err
}

// Update modifies an existing camera.
func (s *CameraStore) Update(c Camera) error {
	c.UpdatedAt = time.Now()
	res, err := s.db.Exec(`UPDATE cameras SET name=?, enabled=?, source_type=?, url=?, username=?,
		password=?, transport=?, record=?, onvif_enabled=?, onvif_xaddr=?, onvif_username=?,
		onvif_password=?, onvif_profile=?, updated_at=? WHERE id=?`,
		c.Name, boolToInt(c.Enabled), string(c.SourceType), c.URL, c.Username, c.Password,
		c.Transport, boolToInt(c.Record), boolToInt(c.OnvifEnabled), c.OnvifXAddr, c.OnvifUsername,
		c.OnvifPassword, c.OnvifProfile, timeToMS(c.UpdatedAt), c.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a camera (and, via cascade, its recordings rows).
func (s *CameraStore) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM cameras WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCamera(sc scanner) (Camera, error) {
	var c Camera
	var enabled, record, onvifEnabled int
	var srcType string
	var createdAt, updatedAt int64
	err := sc.Scan(&c.ID, &c.Name, &enabled, &srcType, &c.URL, &c.Username, &c.Password,
		&c.Transport, &record, &onvifEnabled, &c.OnvifXAddr, &c.OnvifUsername, &c.OnvifPassword,
		&c.OnvifProfile, &createdAt, &updatedAt)
	if err != nil {
		return Camera{}, err
	}
	c.Enabled = enabled != 0
	c.Record = record != 0
	c.OnvifEnabled = onvifEnabled != 0
	c.SourceType = SourceType(srcType)
	c.CreatedAt = msToTime(createdAt)
	c.UpdatedAt = msToTime(updatedAt)
	return c, nil
}
