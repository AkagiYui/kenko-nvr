// Package database provides the SQLite-backed persistence layer.
//
// It uses modernc.org/sqlite, a pure-Go (CGO-free) SQLite driver, so the whole
// binary builds with CGO_ENABLED=0.
package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go sqlite driver, registers "sqlite"
)

// DB wraps the SQL connection and exposes the typed stores.
type DB struct {
	sql        *sql.DB
	Cameras    *CameraStore
	Recordings *RecordingStore
	Settings   *SettingsStore
	Users      *UserStore
	Events     *EventStore
	Push       *PushStore
	Persons    *PersonStore
	Faces      *FaceStore
	FaceTracks *FaceTrackStore
	FaceJobs   *FaceJobStore
}

// Open opens (creating if needed) the SQLite database at path, applies pragmas,
// runs migrations and returns a ready DB.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating db dir: %w", err)
		}
	}

	// WAL + busy_timeout make concurrent reads/writes from the recorder,
	// retention worker and API behave under load. foreign_keys enforces the
	// recordings -> cameras relationship.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}

	// modernc.org/sqlite serialises access per connection; a single writer
	// connection avoids "database is locked" while still allowing the WAL
	// readers to proceed.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("pinging sqlite: %w", err)
	}

	db := &DB{sql: sqlDB}
	if err := db.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrating: %w", err)
	}

	db.Cameras = &CameraStore{db: sqlDB}
	db.Recordings = &RecordingStore{db: sqlDB}
	db.Settings = &SettingsStore{db: sqlDB}
	db.Users = &UserStore{db: sqlDB}
	db.Events = &EventStore{db: sqlDB}
	db.Push = &PushStore{db: sqlDB}
	db.Persons = &PersonStore{db: sqlDB}
	db.Faces = &FaceStore{db: sqlDB}
	db.FaceTracks = &FaceTrackStore{db: sqlDB}
	db.FaceJobs = &FaceJobStore{db: sqlDB}
	return db, nil
}

// SQL exposes the underlying *sql.DB (used by tests).
func (d *DB) SQL() *sql.DB { return d.sql }

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }
