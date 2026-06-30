package database

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

// --- embedding BLOB codec -----------------------------------------------------

// encodeEmbedding packs a float32 vector as little-endian bytes for BLOB storage.
func encodeEmbedding(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeEmbedding unpacks a little-endian float32 BLOB.
func decodeEmbedding(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// --- faces --------------------------------------------------------------------

// FaceStore persists detected faces.
type FaceStore struct {
	db *sql.DB
}

const faceColumns = `id, recording_id, camera_id, person_id, track_id, ts, offset_ms,
	bbox_x, bbox_y, bbox_w, bbox_h, det_score, quality, embedding, dim, model,
	thumb_path, is_exemplar, confirmed, ignored, created_at`

// FaceFilter narrows a faces query.
type FaceFilter struct {
	PersonID      string
	RecordingID   string
	CameraID      string
	TrackID       string
	From          time.Time
	To            time.Time
	OnlyExemplars bool
	// Unassigned restricts to faces with no person and not ignored (review pool).
	Unassigned     bool
	IncludeIgnored bool
	Limit          int
	Offset         int
}

// Create inserts a face.
func (s *FaceStore) Create(f Face) error {
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = MS(time.Now())
	}
	_, err := s.db.Exec(`INSERT INTO faces (`+faceColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.ID, f.RecordingID, f.CameraID, f.PersonID, f.TrackID,
		timeToMS(f.Timestamp.Time), f.OffsetMS,
		f.BBox.X, f.BBox.Y, f.BBox.W, f.BBox.H, f.DetScore, f.Quality,
		encodeEmbedding(f.Embedding), f.Dim, f.Model,
		f.ThumbPath, boolToInt(f.IsExemplar), boolToInt(f.Confirmed), boolToInt(f.Ignored),
		timeToMS(f.CreatedAt.Time))
	return err
}

// Get returns one face by ID (embedding included).
func (s *FaceStore) Get(id string) (Face, error) {
	row := s.db.QueryRow(`SELECT `+faceColumns+` FROM faces WHERE id = ?`, id)
	f, err := scanFace(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Face{}, ErrNotFound
	}
	return f, err
}

// List returns faces matching the filter, newest first.
func (s *FaceStore) List(f FaceFilter) ([]Face, error) {
	var where []string
	var args []any
	if f.PersonID != "" {
		where = append(where, "person_id = ?")
		args = append(args, f.PersonID)
	}
	if f.RecordingID != "" {
		where = append(where, "recording_id = ?")
		args = append(args, f.RecordingID)
	}
	if f.CameraID != "" {
		where = append(where, "camera_id = ?")
		args = append(args, f.CameraID)
	}
	if f.TrackID != "" {
		where = append(where, "track_id = ?")
		args = append(args, f.TrackID)
	}
	if f.OnlyExemplars {
		where = append(where, "is_exemplar = 1")
	}
	if f.Unassigned {
		where = append(where, "person_id = ''")
	}
	if !f.IncludeIgnored {
		where = append(where, "ignored = 0")
	}
	if !f.From.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, timeToMS(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, "ts <= ?")
		args = append(args, timeToMS(f.To))
	}
	q := `SELECT ` + faceColumns + ` FROM faces`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ts DESC"
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
	var out []Face
	for rows.Next() {
		fc, err := scanFace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fc)
	}
	return out, rows.Err()
}

// CountByRecording returns how many faces were stored for a recording.
func (s *FaceStore) CountByRecording(recordingID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM faces WHERE recording_id = ?`, recordingID).Scan(&n)
	return n, err
}

func scanFace(sc scanner) (Face, error) {
	var f Face
	var tsMS, createdMS int64
	var emb []byte
	var isExemplar, confirmed, ignored int
	err := sc.Scan(&f.ID, &f.RecordingID, &f.CameraID, &f.PersonID, &f.TrackID,
		&tsMS, &f.OffsetMS, &f.BBox.X, &f.BBox.Y, &f.BBox.W, &f.BBox.H,
		&f.DetScore, &f.Quality, &emb, &f.Dim, &f.Model,
		&f.ThumbPath, &isExemplar, &confirmed, &ignored, &createdMS)
	if err != nil {
		return Face{}, err
	}
	f.Timestamp = MS(msToTime(tsMS))
	f.CreatedAt = MS(msToTime(createdMS))
	if len(emb) > 0 {
		f.Embedding = decodeEmbedding(emb)
	}
	f.IsExemplar = isExemplar != 0
	f.Confirmed = confirmed != 0
	f.Ignored = ignored != 0
	return f, nil
}

// --- persons ------------------------------------------------------------------

// PersonStore persists discovered identities.
type PersonStore struct {
	db *sql.DB
}

const personColumns = `id, name, notes, cover_face_id, named, face_count,
	first_seen, last_seen, created_at, updated_at`

// PersonFilter narrows a persons query.
type PersonFilter struct {
	NamedOnly bool
	Limit     int
	Offset    int
}

// Create inserts a person.
func (s *PersonStore) Create(p Person) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = MS(now)
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = MS(now)
	}
	_, err := s.db.Exec(`INSERT INTO persons (`+personColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Notes, p.CoverFaceID, boolToInt(p.Named), p.FaceCount,
		timeToMS(p.FirstSeen.Time), timeToMS(p.LastSeen.Time),
		timeToMS(p.CreatedAt.Time), timeToMS(p.UpdatedAt.Time))
	return err
}

// Get returns one person by ID.
func (s *PersonStore) Get(id string) (Person, error) {
	row := s.db.QueryRow(`SELECT `+personColumns+` FROM persons WHERE id = ?`, id)
	p, err := scanPerson(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Person{}, ErrNotFound
	}
	return p, err
}

// List returns persons, most-recently-seen first.
func (s *PersonStore) List(f PersonFilter) ([]Person, error) {
	q := `SELECT ` + personColumns + ` FROM persons`
	var args []any
	if f.NamedOnly {
		q += " WHERE named = 1"
	}
	q += " ORDER BY last_seen DESC"
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
	var out []Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateMeta sets the operator-editable fields (name/notes/cover) and marks the
// person named when a non-empty name is given.
func (s *PersonStore) UpdateMeta(id, name, notes, coverFaceID string) error {
	_, err := s.db.Exec(`UPDATE persons SET name=?, notes=?, cover_face_id=?,
		named=CASE WHEN ?<>'' THEN 1 ELSE named END, updated_at=? WHERE id=?`,
		name, notes, coverFaceID, name, timeToMS(time.Now()), id)
	return err
}

// Delete removes a person row. Faces are detached by the caller first.
func (s *PersonStore) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM persons WHERE id = ?`, id)
	return err
}

// Recount recomputes the denormalised face_count and first/last seen window for
// a person from its assigned, non-ignored faces. Returns the new count.
func (s *PersonStore) Recount(id string) (int, error) {
	var n int
	var minTS, maxTS sql.NullInt64
	err := s.db.QueryRow(`SELECT COUNT(*), MIN(ts), MAX(ts) FROM faces
		WHERE person_id = ? AND ignored = 0`, id).Scan(&n, &minTS, &maxTS)
	if err != nil {
		return 0, err
	}
	_, err = s.db.Exec(`UPDATE persons SET face_count=?, first_seen=?, last_seen=?, updated_at=? WHERE id=?`,
		n, minTS.Int64, maxTS.Int64, timeToMS(time.Now()), id)
	return n, err
}

func scanPerson(sc scanner) (Person, error) {
	var p Person
	var named int
	var firstMS, lastMS, createdMS, updatedMS int64
	err := sc.Scan(&p.ID, &p.Name, &p.Notes, &p.CoverFaceID, &named, &p.FaceCount,
		&firstMS, &lastMS, &createdMS, &updatedMS)
	if err != nil {
		return Person{}, err
	}
	p.Named = named != 0
	p.FirstSeen = MS(msToTime(firstMS))
	p.LastSeen = MS(msToTime(lastMS))
	p.CreatedAt = MS(msToTime(createdMS))
	p.UpdatedAt = MS(msToTime(updatedMS))
	return p, nil
}

// --- jobs ---------------------------------------------------------------------

// FaceJobStore is a durable work queue: one analysis job per recording.
type FaceJobStore struct {
	db *sql.DB
}

// Enqueue adds a pending job for a recording, ignoring duplicates (unique on
// recording_id).
func (s *FaceJobStore) Enqueue(recordingID string) error {
	now := timeToMS(time.Now())
	_, err := s.db.Exec(`INSERT INTO face_jobs (id, recording_id, state, created_at, updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(recording_id) DO NOTHING`,
		uuid.NewString(), recordingID, string(FaceJobPending), now, now)
	return err
}

// ClaimNext atomically takes the oldest runnable job (pending, or failed with
// attempts < maxAttempts), marking it running. ok is false when none is ready.
func (s *FaceJobStore) ClaimNext(maxAttempts int) (FaceJob, bool, error) {
	row := s.db.QueryRow(`SELECT `+faceJobColumns+` FROM face_jobs
		WHERE state = ? OR (state = ? AND attempts < ?)
		ORDER BY created_at ASC LIMIT 1`,
		string(FaceJobPending), string(FaceJobFailed), maxAttempts)
	j, err := scanFaceJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FaceJob{}, false, nil
	}
	if err != nil {
		return FaceJob{}, false, err
	}
	res, err := s.db.Exec(`UPDATE face_jobs SET state=?, attempts=attempts+1, updated_at=?
		WHERE id=? AND state=?`,
		string(FaceJobRunning), timeToMS(time.Now()), j.ID, string(j.State))
	if err != nil {
		return FaceJob{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Lost a race (shouldn't happen with one writer); treat as none ready.
		return FaceJob{}, false, nil
	}
	j.State = FaceJobRunning
	j.Attempts++
	return j, true, nil
}

// Complete marks a job done with its face count.
func (s *FaceJobStore) Complete(id string, faceCount int) error {
	_, err := s.db.Exec(`UPDATE face_jobs SET state=?, face_count=?, error='', updated_at=? WHERE id=?`,
		string(FaceJobDone), faceCount, timeToMS(time.Now()), id)
	return err
}

// Fail marks a job failed with an error message (it may be retried until its
// attempts hit the worker's max).
func (s *FaceJobStore) Fail(id, msg string) error {
	_, err := s.db.Exec(`UPDATE face_jobs SET state=?, error=?, updated_at=? WHERE id=?`,
		string(FaceJobFailed), msg, timeToMS(time.Now()), id)
	return err
}

// SetState forces a job's state (e.g. skipped). Used for terminal non-error
// outcomes like "recording has no local file".
func (s *FaceJobStore) SetState(id string, state FaceJobState, msg string) error {
	_, err := s.db.Exec(`UPDATE face_jobs SET state=?, error=?, updated_at=? WHERE id=?`,
		string(state), msg, timeToMS(time.Now()), id)
	return err
}

// RequeueRunning resets jobs stuck in "running" (e.g. after a crash) back to
// pending. Call once at worker startup. Returns the number reset.
func (s *FaceJobStore) RequeueRunning() (int64, error) {
	res, err := s.db.Exec(`UPDATE face_jobs SET state=?, updated_at=? WHERE state=?`,
		string(FaceJobPending), timeToMS(time.Now()), string(FaceJobRunning))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Counts returns the number of jobs in each state (for status/debug).
func (s *FaceJobStore) Counts() (map[FaceJobState]int, error) {
	rows, err := s.db.Query(`SELECT state, COUNT(*) FROM face_jobs GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[FaceJobState]int)
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[FaceJobState(st)] = n
	}
	return out, rows.Err()
}

const faceJobColumns = `id, recording_id, state, attempts, error, face_count, created_at, updated_at`

func scanFaceJob(sc scanner) (FaceJob, error) {
	var j FaceJob
	var state string
	var createdMS, updatedMS int64
	err := sc.Scan(&j.ID, &j.RecordingID, &state, &j.Attempts, &j.Error, &j.FaceCount, &createdMS, &updatedMS)
	if err != nil {
		return FaceJob{}, err
	}
	j.State = FaceJobState(state)
	j.CreatedAt = MS(msToTime(createdMS))
	j.UpdatedAt = MS(msToTime(updatedMS))
	return j, nil
}
