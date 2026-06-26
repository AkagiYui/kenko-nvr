package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// authenticator issues and validates opaque in-memory bearer tokens bound to a
// database-backed user (with a role). Tokens do not survive a restart (users log
// in again), which keeps the design simple and avoids persisting secrets.
type authenticator struct {
	users *database.UserStore

	mu     sync.Mutex
	tokens map[string]*session
}

// session is the per-token authentication context.
type session struct {
	UserID   string
	Username string
	Role     database.Role
	exp      time.Time
}

type ctxKey int

const sessionCtxKey ctxKey = iota

const tokenTTL = 12 * time.Hour

func newAuthenticator(users *database.UserStore) *authenticator {
	return &authenticator{
		users:  users,
		tokens: make(map[string]*session),
	}
}

// hashPassword returns a bcrypt hash of password.
func hashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

// checkPassword reports whether password matches the stored bcrypt hash.
func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func (a *authenticator) issue(u database.User) string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	tok := hex.EncodeToString(buf)
	a.mu.Lock()
	a.tokens[tok] = &session{
		UserID:   u.ID,
		Username: u.Username,
		Role:     u.Role,
		exp:      time.Now().Add(tokenTTL),
	}
	// opportunistic cleanup
	now := time.Now()
	for t, s := range a.tokens {
		if now.After(s.exp) {
			delete(a.tokens, t)
		}
	}
	a.mu.Unlock()
	return tok
}

func (a *authenticator) valid(tok string) (*session, bool) {
	if tok == "" {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.tokens[tok]
	if !ok {
		return nil, false
	}
	if time.Now().After(s.exp) {
		delete(a.tokens, tok)
		return nil, false
	}
	return s, true
}

// revoke drops a token (logout).
func (a *authenticator) revoke(tok string) {
	a.mu.Lock()
	delete(a.tokens, tok)
	a.mu.Unlock()
}

// revokeUser drops every token of a user (e.g. after delete / role change).
func (a *authenticator) revokeUser(userID string) {
	a.mu.Lock()
	for t, s := range a.tokens {
		if s.UserID == userID {
			delete(a.tokens, t)
		}
	}
	a.mu.Unlock()
}

// middleware authenticates via the Authorization: Bearer header.
func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := a.valid(bearer(r))
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(withSession(r.Context(), s)))
	})
}

// mediaMiddleware also accepts the token via the ?token= query parameter, for
// media URLs loaded by <video>/hls.js where custom headers are awkward.
func (a *authenticator) mediaMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		s, ok := a.valid(tok)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(withSession(r.Context(), s)))
	})
}

// requireRole returns middleware that rejects users below the given role.
func requireRole(min database.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s := sessionFrom(r)
			if s == nil || roleRank(s.Role) < roleRank(min) {
				writeErr(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func roleRank(r database.Role) int {
	switch r {
	case database.RoleAdmin:
		return 2
	case database.RoleOperator:
		return 1
	default:
		return 0
	}
}

func withSession(ctx context.Context, s *session) context.Context {
	return context.WithValue(ctx, sessionCtxKey, s)
}

func sessionFrom(r *http.Request) *session {
	s, _ := r.Context().Value(sessionCtxKey).(*session)
	return s
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	user, err := s.db.Users.GetByUsername(strings.TrimSpace(req.Username))
	if err != nil || !checkPassword(user.PasswordHash, req.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":    s.auth.issue(user),
		"username": user.Username,
		"role":     user.Role,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.revoke(bearer(r))
	w.WriteHeader(http.StatusNoContent)
}

// handleMe returns the current user's identity and role.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if sess == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       sess.UserID,
		"username": sess.Username,
		"role":     sess.Role,
	})
}

// ensureSeedAdmin creates an initial admin account from the bootstrap config
// when no users exist yet, so a fresh install can still log in.
func ensureSeedAdmin(db *database.DB, username, password string) error {
	n, err := db.Users.Count()
	if err != nil || n > 0 {
		return err
	}
	if username == "" {
		username = "admin"
	}
	if password == "" {
		password = "admin"
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return db.Users.Create(database.User{
		ID:           uuid.NewString(),
		Username:     username,
		PasswordHash: hash,
		Role:         database.RoleAdmin,
	})
}
