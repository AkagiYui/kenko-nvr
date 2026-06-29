package database

import (
	"database/sql"
	"strings"
	"time"
)

// EventStore persists detected events (motion).
type EventStore struct {
	db *sql.DB
}

const eventColumns = `id, camera_id, type, start_time, end_time, score, created_at`

// EventFilter narrows an events query.
type EventFilter struct {
	CameraID string
	Type     EventType
	From     time.Time
	To       time.Time
	Limit    int
}

// Create inserts a new event.
func (s *EventStore) Create(e Event) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = MS(time.Now())
	}
	if e.Type == "" {
		e.Type = EventMotion
	}
	_, err := s.db.Exec(`INSERT INTO events (`+eventColumns+`) VALUES (?,?,?,?,?,?,?)`,
		e.ID, e.CameraID, string(e.Type), timeToMS(e.StartTime.Time), timeToMS(e.EndTime.Time),
		e.Score, timeToMS(e.CreatedAt.Time))
	return err
}

// Finalize sets an event's end time and score.
func (s *EventStore) Finalize(id string, end time.Time, score float64) error {
	_, err := s.db.Exec(`UPDATE events SET end_time=?, score=? WHERE id=?`, timeToMS(end), score, id)
	return err
}

// List returns events matching the filter, newest first.
func (s *EventStore) List(f EventFilter) ([]Event, error) {
	var where []string
	var args []any
	if f.CameraID != "" {
		where = append(where, "camera_id = ?")
		args = append(args, f.CameraID)
	}
	if f.Type != "" {
		where = append(where, "type = ?")
		args = append(args, string(f.Type))
	}
	if !f.From.IsZero() {
		where = append(where, "start_time >= ?")
		args = append(args, timeToMS(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, "start_time <= ?")
		args = append(args, timeToMS(f.To))
	}
	q := `SELECT ` + eventColumns + ` FROM events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY start_time DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteOlderThan removes events that started before cutoff. Returns rows deleted.
func (s *EventStore) DeleteOlderThan(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM events WHERE start_time < ?`, timeToMS(cutoff))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func scanEvent(sc scanner) (Event, error) {
	var e Event
	var typ string
	var startMS, endMS, createdMS int64
	if err := sc.Scan(&e.ID, &e.CameraID, &typ, &startMS, &endMS, &e.Score, &createdMS); err != nil {
		return Event{}, err
	}
	e.Type = EventType(typ)
	e.StartTime = MS(msToTime(startMS))
	e.EndTime = MS(msToTime(endMS))
	e.CreatedAt = MS(msToTime(createdMS))
	return e, nil
}

// --- Push subscriptions -------------------------------------------------------

// PushStore persists Web Push subscriptions.
type PushStore struct {
	db *sql.DB
}

// Upsert stores (or refreshes) a subscription keyed by endpoint.
func (s *PushStore) Upsert(p PushSubscription) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = MS(time.Now())
	}
	_, err := s.db.Exec(`INSERT INTO push_subscriptions (id, endpoint, p256dh, auth, created_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh=excluded.p256dh, auth=excluded.auth`,
		p.ID, p.Endpoint, p.P256dh, p.Auth, timeToMS(p.CreatedAt.Time))
	return err
}

// List returns all push subscriptions.
func (s *PushStore) List() ([]PushSubscription, error) {
	rows, err := s.db.Query(`SELECT id, endpoint, p256dh, auth, created_at FROM push_subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		var p PushSubscription
		var createdMS int64
		if err := rows.Scan(&p.ID, &p.Endpoint, &p.P256dh, &p.Auth, &createdMS); err != nil {
			return nil, err
		}
		p.CreatedAt = MS(msToTime(createdMS))
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteByEndpoint removes a subscription (e.g. after a 410 Gone from the push
// service).
func (s *PushStore) DeleteByEndpoint(endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}
