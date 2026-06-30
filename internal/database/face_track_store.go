package database

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// FaceTrack is a group of faces the tracker linked as one appearance within a
// recording, with a representative embedding used for gallery matching and
// clustering.
type FaceTrack struct {
	ID          string  `json:"id"`
	RecordingID string  `json:"recordingId"`
	CameraID    string  `json:"cameraId"`
	PersonID    string  `json:"personId"`
	StartTS     EpochMS `json:"startTs"`
	EndTS       EpochMS `json:"endTs"`
	FaceCount   int     `json:"faceCount"`
	Quality     float64 `json:"quality"`
	// Embedding is the quality-weighted, L2-normalised representative. Biometric:
	// never serialised.
	Embedding []float32 `json:"-"`
	Dim       int       `json:"-"`
	// BestFaceID / BestOffsetMS point at the sharpest face of the track, used as
	// the appearance thumbnail and the seek target for jump-to-playback.
	BestFaceID   string `json:"bestFaceId"`
	BestOffsetMS int64  `json:"bestOffsetMs"`
	// CandPersonID / CandSim record the nearest gallery candidate at assignment
	// time (even if below the match threshold), for merge suggestions.
	CandPersonID string  `json:"-"`
	CandSim      float64 `json:"candSim"`
	Confirmed    bool    `json:"confirmed"`
	CreatedAt    EpochMS `json:"createdAt"`
}

// FaceTrackStore persists face tracks.
type FaceTrackStore struct {
	db *sql.DB
}

const faceTrackColumns = `id, recording_id, camera_id, person_id, start_ts, end_ts,
	face_count, quality, rep_embedding, dim, best_face_id, best_offset_ms,
	cand_person_id, cand_sim, confirmed, created_at`

// FaceTrackFilter narrows a tracks query.
type FaceTrackFilter struct {
	PersonID    string
	RecordingID string
	CameraID    string
	Unassigned  bool
	From        time.Time
	To          time.Time
	Limit       int
	Offset      int
}

// Create inserts a track.
func (s *FaceTrackStore) Create(t FaceTrack) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = MS(time.Now())
	}
	_, err := s.db.Exec(`INSERT INTO face_tracks (`+faceTrackColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.RecordingID, t.CameraID, t.PersonID,
		timeToMS(t.StartTS.Time), timeToMS(t.EndTS.Time), t.FaceCount, t.Quality,
		encodeEmbedding(t.Embedding), t.Dim, t.BestFaceID, t.BestOffsetMS,
		t.CandPersonID, t.CandSim, boolToInt(t.Confirmed), timeToMS(t.CreatedAt.Time))
	return err
}

// Get returns one track by ID.
func (s *FaceTrackStore) Get(id string) (FaceTrack, error) {
	row := s.db.QueryRow(`SELECT `+faceTrackColumns+` FROM face_tracks WHERE id = ?`, id)
	t, err := scanFaceTrack(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FaceTrack{}, ErrNotFound
	}
	return t, err
}

// List returns tracks matching the filter, newest first.
func (s *FaceTrackStore) List(f FaceTrackFilter) ([]FaceTrack, error) {
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
	if f.Unassigned {
		where = append(where, "person_id = ''")
	}
	if !f.From.IsZero() {
		where = append(where, "start_ts >= ?")
		args = append(args, timeToMS(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, "start_ts <= ?")
		args = append(args, timeToMS(f.To))
	}
	q := `SELECT ` + faceTrackColumns + ` FROM face_tracks`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY start_ts DESC"
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
	var out []FaceTrack
	for rows.Next() {
		t, err := scanFaceTrack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdatePerson reassigns a track to a person (or "" to detach).
func (s *FaceTrackStore) UpdatePerson(id, personID string) error {
	_, err := s.db.Exec(`UPDATE face_tracks SET person_id = ? WHERE id = ?`, personID, id)
	return err
}

// ReassignPerson moves every track from one person to another (used by merge).
func (s *FaceTrackStore) ReassignPerson(fromPersonID, toPersonID string) error {
	_, err := s.db.Exec(`UPDATE face_tracks SET person_id=? WHERE person_id=?`, toPersonID, fromPersonID)
	return err
}

// SetConfirmed marks a track's assignment as operator-locked.
func (s *FaceTrackStore) SetConfirmed(id string, confirmed bool) error {
	_, err := s.db.Exec(`UPDATE face_tracks SET confirmed = ? WHERE id = ?`, boolToInt(confirmed), id)
	return err
}

// CountByRecording returns how many tracks exist for a recording (used to make
// assignment idempotent).
func (s *FaceTrackStore) CountByRecording(recordingID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM face_tracks WHERE recording_id = ?`, recordingID).Scan(&n)
	return n, err
}

// DeleteByRecording removes a recording's tracks (used when re-analysing).
func (s *FaceTrackStore) DeleteByRecording(recordingID string) error {
	_, err := s.db.Exec(`DELETE FROM face_tracks WHERE recording_id = ?`, recordingID)
	return err
}

func scanFaceTrack(sc scanner) (FaceTrack, error) {
	var t FaceTrack
	var startMS, endMS, createdMS int64
	var emb []byte
	var confirmed int
	err := sc.Scan(&t.ID, &t.RecordingID, &t.CameraID, &t.PersonID,
		&startMS, &endMS, &t.FaceCount, &t.Quality, &emb, &t.Dim,
		&t.BestFaceID, &t.BestOffsetMS, &t.CandPersonID, &t.CandSim, &confirmed, &createdMS)
	if err != nil {
		return FaceTrack{}, err
	}
	t.StartTS = MS(msToTime(startMS))
	t.EndTS = MS(msToTime(endMS))
	t.CreatedAt = MS(msToTime(createdMS))
	if len(emb) > 0 {
		t.Embedding = decodeEmbedding(emb)
	}
	t.Confirmed = confirmed != 0
	return t, nil
}
