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

	// --- v11: GB28181 device/channel binding --------------------------------
	`ALTER TABLE cameras ADD COLUMN gb28181_device_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE cameras ADD COLUMN gb28181_channel_id TEXT NOT NULL DEFAULT ''`,

	// --- v12: cloud-only recordings -----------------------------------------
	// local_removed marks a recording whose local file has been deleted (by
	// retention or the upload worker) but which is preserved on S3 and remains
	// playable by streaming it back through the NVR. The row is kept so the clip
	// stays discoverable; it is excluded from local-disk size accounting and from
	// the retention worker's deletion candidates.
	`ALTER TABLE recordings ADD COLUMN local_removed INTEGER NOT NULL DEFAULT 0`,

	// --- v13: client-side encrypted S3 objects ------------------------------
	// encrypted marks a recording whose S3 object is client-side encrypted and
	// must be decrypted on download/playback.
	`ALTER TABLE recordings ADD COLUMN encrypted INTEGER NOT NULL DEFAULT 0`,

	// --- v14: face recognition — persons, faces, jobs -----------------------
	// persons are discovered identities; faces are per-frame detections carrying
	// a 512-d ArcFace embedding (little-endian float32 BLOB); face_jobs is the
	// durable post-process work queue (one analysis job per recording).
	`CREATE TABLE IF NOT EXISTS persons (
		id            TEXT PRIMARY KEY,
		name          TEXT NOT NULL DEFAULT '',
		notes         TEXT NOT NULL DEFAULT '',
		cover_face_id TEXT NOT NULL DEFAULT '',
		named         INTEGER NOT NULL DEFAULT 0,
		face_count    INTEGER NOT NULL DEFAULT 0,
		first_seen    INTEGER NOT NULL DEFAULT 0,
		last_seen     INTEGER NOT NULL DEFAULT 0,
		created_at    INTEGER NOT NULL DEFAULT 0,
		updated_at    INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS faces (
		id           TEXT PRIMARY KEY,
		recording_id TEXT NOT NULL,
		camera_id    TEXT NOT NULL DEFAULT '',
		person_id    TEXT NOT NULL DEFAULT '',
		track_id     TEXT NOT NULL DEFAULT '',
		ts           INTEGER NOT NULL DEFAULT 0,
		offset_ms    INTEGER NOT NULL DEFAULT 0,
		bbox_x       REAL NOT NULL DEFAULT 0,
		bbox_y       REAL NOT NULL DEFAULT 0,
		bbox_w       REAL NOT NULL DEFAULT 0,
		bbox_h       REAL NOT NULL DEFAULT 0,
		det_score    REAL NOT NULL DEFAULT 0,
		quality      REAL NOT NULL DEFAULT 0,
		embedding    BLOB,
		dim          INTEGER NOT NULL DEFAULT 0,
		model        TEXT NOT NULL DEFAULT '',
		thumb_path   TEXT NOT NULL DEFAULT '',
		is_exemplar  INTEGER NOT NULL DEFAULT 0,
		confirmed    INTEGER NOT NULL DEFAULT 0,
		ignored      INTEGER NOT NULL DEFAULT 0,
		created_at   INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (recording_id) REFERENCES recordings(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_faces_person ON faces(person_id, ts)`,
	`CREATE INDEX IF NOT EXISTS idx_faces_recording ON faces(recording_id)`,
	`CREATE INDEX IF NOT EXISTS idx_faces_camera_ts ON faces(camera_id, ts)`,
	`CREATE INDEX IF NOT EXISTS idx_faces_track ON faces(track_id)`,
	`CREATE TABLE IF NOT EXISTS face_jobs (
		id           TEXT PRIMARY KEY,
		recording_id TEXT NOT NULL,
		state        TEXT NOT NULL DEFAULT 'pending',
		attempts     INTEGER NOT NULL DEFAULT 0,
		error        TEXT NOT NULL DEFAULT '',
		face_count   INTEGER NOT NULL DEFAULT 0,
		created_at   INTEGER NOT NULL DEFAULT 0,
		updated_at   INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (recording_id) REFERENCES recordings(id) ON DELETE CASCADE
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_face_jobs_recording ON face_jobs(recording_id)`,
	`CREATE INDEX IF NOT EXISTS idx_face_jobs_state ON face_jobs(state, created_at)`,

	// --- v15: face tracks ---------------------------------------------------
	// A track groups the faces the tracker linked as one appearance within a
	// recording. It carries a quality-weighted representative embedding and is
	// the unit that gets matched to a person and (phase 3) re-clustered.
	// cand_person_id/cand_sim record the nearest non-assigned candidate, so the
	// review/merge UI can suggest joins even when the match was below threshold.
	`CREATE TABLE IF NOT EXISTS face_tracks (
		id             TEXT PRIMARY KEY,
		recording_id   TEXT NOT NULL,
		camera_id      TEXT NOT NULL DEFAULT '',
		person_id      TEXT NOT NULL DEFAULT '',
		start_ts       INTEGER NOT NULL DEFAULT 0,
		end_ts         INTEGER NOT NULL DEFAULT 0,
		face_count     INTEGER NOT NULL DEFAULT 0,
		quality        REAL NOT NULL DEFAULT 0,
		rep_embedding  BLOB,
		dim            INTEGER NOT NULL DEFAULT 0,
		best_face_id   TEXT NOT NULL DEFAULT '',
		best_offset_ms INTEGER NOT NULL DEFAULT 0,
		cand_person_id TEXT NOT NULL DEFAULT '',
		cand_sim       REAL NOT NULL DEFAULT 0,
		confirmed      INTEGER NOT NULL DEFAULT 0,
		created_at     INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (recording_id) REFERENCES recordings(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_face_tracks_person ON face_tracks(person_id)`,
	`CREATE INDEX IF NOT EXISTS idx_face_tracks_recording ON face_tracks(recording_id)`,
	`CREATE INDEX IF NOT EXISTS idx_face_tracks_camera_ts ON face_tracks(camera_id, start_ts)`,

	// --- v16: clustering constraints ----------------------------------------
	// person_links records operator corrections as track-level constraints that
	// re-clustering must honour: must = force two tracks into one identity,
	// cannot = keep them apart.
	`CREATE TABLE IF NOT EXISTS person_links (
		id         TEXT PRIMARY KEY,
		kind       TEXT NOT NULL,
		a_track    TEXT NOT NULL DEFAULT '',
		b_track    TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_person_links_a ON person_links(a_track)`,
	`CREATE INDEX IF NOT EXISTS idx_person_links_b ON person_links(b_track)`,
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
