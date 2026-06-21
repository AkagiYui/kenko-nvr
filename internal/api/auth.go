package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

// authenticator issues and validates opaque in-memory bearer tokens. Tokens do
// not survive a restart (users simply log in again), which keeps the design
// simple and avoids persisting secrets.
type authenticator struct {
	username string
	password string

	mu     sync.Mutex
	tokens map[string]time.Time // token -> expiry
}

const tokenTTL = 12 * time.Hour

func newAuthenticator(username, password string) *authenticator {
	return &authenticator{
		username: username,
		password: password,
		tokens:   make(map[string]time.Time),
	}
}

func (a *authenticator) check(username, password string) bool {
	u := subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) == 1
	p := subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1
	return u && p
}

func (a *authenticator) issue() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	tok := hex.EncodeToString(buf)
	a.mu.Lock()
	a.tokens[tok] = time.Now().Add(tokenTTL)
	// opportunistic cleanup
	now := time.Now()
	for t, exp := range a.tokens {
		if now.After(exp) {
			delete(a.tokens, t)
		}
	}
	a.mu.Unlock()
	return tok
}

func (a *authenticator) valid(tok string) bool {
	if tok == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.tokens[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(a.tokens, tok)
		return false
	}
	return true
}

// middleware authenticates via the Authorization: Bearer header.
func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.valid(bearer(r)) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
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
		if !a.valid(tok) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
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
	if !s.auth.check(req.Username, req.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": s.auth.issue()})
}
