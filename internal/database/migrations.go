package database

// migrations are applied in order; schema_version tracks how many have run.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS cameras (
		id              TEXT PRIMARY KEY,
		name            TEXT NOT NULL,
		enabled         INTEGER NOT NULL DEFAULT 1,
		source_type     TEXT NOT NULL DEFAULT 'rtsp',
		url             TEXT NOT NULL DEFAULT '',
		username        TEXT NOT NULL DEFAULT '',
		password        TEXT NOT NULL DEFAULT '',
		transport       TEXT NOT NULL DEFAULT '',
		record          INTEGER NOT NULL DEFAULT 0,
		onvif_enabled   INTEGER NOT NULL DEFAULT 0,
		onvif_xaddr     TEXT NOT NULL DEFAULT '',
		onvif_username  TEXT NOT NULL DEFAULT '',
		onvif_password  TEXT NOT NULL DEFAULT '',
		onvif_profile   TEXT NOT NULL DEFAULT '',
		created_at      INTEGER NOT NULL DEFAULT 0,
		updated_at      INTEGER NOT NULL DEFAULT 0
	)`,

	`CREATE TABLE IF NOT EXISTS recordings (
		id          TEXT PRIMARY KEY,
		camera_id   TEXT NOT NULL,
		path        TEXT NOT NULL,
		start_time  INTEGER NOT NULL,
		end_time    INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		size_bytes  INTEGER NOT NULL DEFAULT 0,
		complete    INTEGER NOT NULL DEFAULT 0,
		uploaded    INTEGER NOT NULL DEFAULT 0,
		s3_key      TEXT NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (camera_id) REFERENCES cameras(id) ON DELETE CASCADE
	)`,

	`CREATE INDEX IF NOT EXISTS idx_recordings_camera ON recordings(camera_id, start_time)`,
	`CREATE INDEX IF NOT EXISTS idx_recordings_complete ON recordings(complete, start_time)`,
	`CREATE INDEX IF NOT EXISTS idx_recordings_upload ON recordings(complete, uploaded)`,

	`CREATE TABLE IF NOT EXISTS settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,

	// --- v7: multi-user / RBAC ----------------------------------------------
	`CREATE TABLE IF NOT EXISTS users (
		id            TEXT PRIMARY KEY,
		username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT NOT NULL,
		role          TEXT NOT NULL DEFAULT 'viewer',
		created_at    INTEGER NOT NULL DEFAULT 0,
		updated_at    INTEGER NOT NULL DEFAULT 0
	)`,

	// --- v8: motion events --------------------------------------------------
	`CREATE TABLE IF NOT EXISTS events (
		id          TEXT PRIMARY KEY,
		camera_id   TEXT NOT NULL,
		type        TEXT NOT NULL DEFAULT 'motion',
		start_time  INTEGER NOT NULL,
		end_time    INTEGER NOT NULL DEFAULT 0,
		score       REAL NOT NULL DEFAULT 0,
		created_at  INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (camera_id) REFERENCES cameras(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_events_camera ON events(camera_id, start_time)`,

	// --- v9: web-push subscriptions -----------------------------------------
	`CREATE TABLE IF NOT EXISTS push_subscriptions (
		id         TEXT PRIMARY KEY,
		endpoint   TEXT NOT NULL UNIQUE,
		p256dh     TEXT NOT NULL,
		auth       TEXT NOT NULL,
		created_at INTEGER NOT NULL DEFAULT 0
	)`,

	// --- v10: per-camera motion / event-recording settings ------------------
	`ALTER TABLE cameras ADD COLUMN motion_enabled INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE cameras ADD COLUMN record_mode TEXT NOT NULL DEFAULT 'continuous'`,
	`ALTER TABLE cameras ADD COLUMN motion_sensitivity INTEGER NOT NULL DEFAULT 50`,
}

func (d *DB) migrate() error {
	if _, err := d.sql.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}

	var version int
	row := d.sql.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`)
	if err := row.Scan(&version); err != nil {
		return err
	}

	for i := version; i < len(migrations); i++ {
		tx, err := d.sql.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, i+1); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
