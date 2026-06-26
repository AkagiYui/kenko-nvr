package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

func TestPasswordHashing(t *testing.T) {
	hash, err := hashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "s3cret" {
		t.Error("password stored in plaintext")
	}
	if !checkPassword(hash, "s3cret") {
		t.Error("correct password rejected")
	}
	if checkPassword(hash, "wrong") {
		t.Error("wrong password accepted")
	}
}

func TestRoleRank(t *testing.T) {
	if !(roleRank(database.RoleAdmin) > roleRank(database.RoleOperator)) {
		t.Error("admin should outrank operator")
	}
	if !(roleRank(database.RoleOperator) > roleRank(database.RoleViewer)) {
		t.Error("operator should outrank viewer")
	}
}

func TestRequireRole(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := requireRole(database.RoleOperator)(next)

	cases := []struct {
		name string
		role *database.Role
		want int
	}{
		{"no session", nil, http.StatusForbidden},
		{"viewer", rolePtr(database.RoleViewer), http.StatusForbidden},
		{"operator", rolePtr(database.RoleOperator), http.StatusOK},
		{"admin", rolePtr(database.RoleAdmin), http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.role != nil {
				r = r.WithContext(withSession(r.Context(), &session{Role: *c.role}))
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)
			if rr.Code != c.want {
				t.Errorf("role=%v: status = %d, want %d", c.role, rr.Code, c.want)
			}
		})
	}
}

func TestTokenIssueValidateRevoke(t *testing.T) {
	a := newAuthenticator(nil)
	tok := a.issue(database.User{ID: "u1", Username: "bob", Role: database.RoleOperator})

	s, ok := a.valid(tok)
	if !ok || s.Username != "bob" || s.Role != database.RoleOperator {
		t.Fatalf("issued token should validate to its session, got ok=%v %+v", ok, s)
	}
	a.revoke(tok)
	if _, ok := a.valid(tok); ok {
		t.Error("revoked token should be invalid")
	}

	// revokeUser drops all of a user's tokens.
	t1 := a.issue(database.User{ID: "u2", Username: "carol", Role: database.RoleViewer})
	t2 := a.issue(database.User{ID: "u2", Username: "carol", Role: database.RoleViewer})
	a.revokeUser("u2")
	if _, ok := a.valid(t1); ok {
		t.Error("revokeUser should invalidate t1")
	}
	if _, ok := a.valid(t2); ok {
		t.Error("revokeUser should invalidate t2")
	}
}

func rolePtr(r database.Role) *database.Role { return &r }
