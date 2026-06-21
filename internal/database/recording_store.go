package database

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// RecordingStore persists recording metadata.
type RecordingStore struct {
	db *sql.DB
}

// RecordingFilter narrows a recordings query.
type RecordingFilter struct {
	CameraID string
	From     time.Time
	To       time.Time
	Limit    int
	Offset   int
}

// Create inserts a new (in-progress) recording.
func (s *RecordingStore) Create(r Recording) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO recordings (id, camera_id, path, start_time, end_time,
		duration_ms, size_bytes, complete, uploaded, s3_key, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.CameraID, r.Path, timeToMS(r.StartTime), timeToMS(r.EndTime),
		r.DurationMS, r.SizeBytes, boolToInt(r.Complete), boolToInt(r.Uploaded),
		r.S3Key, timeToMS(r.CreatedAt))
	return err
}

// Finalize marks a recording complete with its final timing and size.
func (s *RecordingStore) Finalize(id string, endTime time.Time, durationMS, sizeBytes int64) error {
	_, err := s.db.Exec(`UPDATE recordings SET end_time=?, duration_ms=?, size_bytes=?, complete=1 WHERE id=?`,
		timeToMS(endTime), durationMS, sizeBytes, id)
	return err
}

// MarkUploaded records a successful S3 upload.
func (s *RecordingStore) MarkUploaded(id, s3Key string) error {
	_, err := s.db.Exec(`UPDATE recordings SET uploaded=1, s3_key=? WHERE id=?`, s3Key, id)
	return err
}

// Get returns one recording by ID.
func (s *RecordingStore) Get(id string) (Recording, error) {
	row := s.db.QueryRow(recordingSelect+` WHERE id = ?`, id)
	r, err := scanRecording(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Recording{}, ErrNotFound
	}
	return r, err
}

// Delete removes a recording row.
func (s *RecordingStore) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM recordings WHERE id = ?`, id)
	return err
}

const recordingSelect = `SELECT id, camera_id, path, start_time, end_time, duration_ms,
	size_bytes, complete, uploaded, s3_key, created_at FROM recordings`

// List returns recordings matching the filter, newest first.
func (s *RecordingStore) List(f RecordingFilter) ([]Recording, error) {
	var where []string
	var args []any
	if f.CameraID != "" {
		where = append(where, "camera_id = ?")
		args = append(args, f.CameraID)
	}
	if !f.From.IsZero() {
		where = append(where, "start_time >= ?")
		args = append(args, timeToMS(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, "start_time <= ?")
		args = append(args, timeToMS(f.To))
	}

	q := recordingSelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY start_time DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
		if f.Offset > 0 {
			q += " OFFSET ?"
			args = append(args, f.Offset)
		}
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Recording
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingUploads returns completed recordings not yet uploaded, oldest first.
func (s *RecordingStore) PendingUploads(limit int) ([]Recording, error) {
	rows, err := s.db.Query(recordingSelect+
		` WHERE complete = 1 AND uploaded = 0 ORDER BY start_time ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Recording
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OldestComplete returns completed recordings ordered oldest first, used by the
// retention worker. onlyUploaded limits the result to already-uploaded files.
func (s *RecordingStore) OldestComplete(limit int, onlyUploaded bool) ([]Recording, error) {
	q := recordingSelect + ` WHERE complete = 1`
	if onlyUploaded {
		q += ` AND uploaded = 1`
	}
	q += ` ORDER BY start_time ASC LIMIT ?`
	rows, err := s.db.Query(q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Recording
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TotalSize returns the summed size of all completed recordings in bytes.
func (s *RecordingStore) TotalSize() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(size_bytes),0) FROM recordings WHERE complete = 1`).Scan(&total)
	return total.Int64, err
}

func scanRecording(sc scanner) (Recording, error) {
	var r Recording
	var startMS, endMS, createdMS int64
	var complete, uploaded int
	err := sc.Scan(&r.ID, &r.CameraID, &r.Path, &startMS, &endMS, &r.DurationMS,
		&r.SizeBytes, &complete, &uploaded, &r.S3Key, &createdMS)
	if err != nil {
		return Recording{}, err
	}
	r.StartTime = msToTime(startMS)
	r.EndTime = msToTime(endMS)
	r.CreatedAt = msToTime(createdMS)
	r.Complete = complete != 0
	r.Uploaded = uploaded != 0
	return r, nil
}
