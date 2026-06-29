package database

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrDuplicate is returned when a unique constraint (e.g. username) is violated.
var ErrDuplicate = errors.New("already exists")

// UserStore persists login accounts.
type UserStore struct {
	db *sql.DB
}

const userColumns = `id, username, password_hash, role, created_at, updated_at`

// Count returns the number of users (used to seed the first admin).
func (s *UserStore) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// List returns all users ordered by username.
func (s *UserStore) List() ([]User, error) {
	rows, err := s.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Get returns one user by ID.
func (s *UserStore) Get(id string) (User, error) {
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// GetByUsername returns one user by username (case-insensitive).
func (s *UserStore) GetByUsername(username string) (User, error) {
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE username = ? COLLATE NOCASE`, username)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// Create inserts a new user.
func (s *UserStore) Create(u User) error {
	now := time.Now()
	u.CreatedAt, u.UpdatedAt = MS(now), MS(now)
	_, err := s.db.Exec(`INSERT INTO users (`+userColumns+`) VALUES (?,?,?,?,?,?)`,
		u.ID, u.Username, u.PasswordHash, string(u.Role), timeToMS(u.CreatedAt.Time), timeToMS(u.UpdatedAt.Time))
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	return err
}

// Update changes a user's username and role; if passwordHash is non-empty it is
// also updated.
func (s *UserStore) Update(id, username string, role Role, passwordHash string) error {
	now := timeToMS(time.Now())
	var (
		res sql.Result
		err error
	)
	if passwordHash != "" {
		res, err = s.db.Exec(`UPDATE users SET username=?, role=?, password_hash=?, updated_at=? WHERE id=?`,
			username, string(role), passwordHash, now, id)
	} else {
		res, err = s.db.Exec(`UPDATE users SET username=?, role=?, updated_at=? WHERE id=?`,
			username, string(role), now, id)
	}
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a user.
func (s *UserStore) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanUser(sc scanner) (User, error) {
	var u User
	var role string
	var createdAt, updatedAt int64
	if err := sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &createdAt, &updatedAt); err != nil {
		return User{}, err
	}
	u.Role = Role(role)
	u.CreatedAt = MS(msToTime(createdAt))
	u.UpdatedAt = MS(msToTime(updatedAt))
	return u, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
