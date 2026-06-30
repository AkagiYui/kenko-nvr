package database

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// PersonLinkStore persists clustering constraints (must/cannot links).
type PersonLinkStore struct {
	db *sql.DB
}

// Create inserts a constraint, returning its ID.
func (s *PersonLinkStore) Create(l PersonLink) (string, error) {
	if l.ID == "" {
		l.ID = uuid.NewString()
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = MS(time.Now())
	}
	_, err := s.db.Exec(`INSERT INTO person_links (id, kind, a_track, b_track, created_at)
		VALUES (?,?,?,?,?)`,
		l.ID, string(l.Kind), l.ATrack, l.BTrack, timeToMS(l.CreatedAt.Time))
	return l.ID, err
}

// List returns all constraints.
func (s *PersonLinkStore) List() ([]PersonLink, error) {
	rows, err := s.db.Query(`SELECT id, kind, a_track, b_track, created_at FROM person_links`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonLink
	for rows.Next() {
		var l PersonLink
		var kind string
		var createdMS int64
		if err := rows.Scan(&l.ID, &kind, &l.ATrack, &l.BTrack, &createdMS); err != nil {
			return nil, err
		}
		l.Kind = LinkKind(kind)
		l.CreatedAt = MS(msToTime(createdMS))
		out = append(out, l)
	}
	return out, rows.Err()
}

// Delete removes a constraint.
func (s *PersonLinkStore) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM person_links WHERE id = ?`, id)
	return err
}
