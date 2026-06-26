package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.Users.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []database.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

type userInput struct {
	Username string        `json:"username"`
	Password string        `json:"password"`
	Role     database.Role `json:"role"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in userInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" {
		writeErr(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(in.Password) < 4 {
		writeErr(w, http.StatusBadRequest, "password must be at least 4 characters")
		return
	}
	if !database.ValidRole(in.Role) {
		writeErr(w, http.StatusBadRequest, "role must be admin, operator or viewer")
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	u := database.User{ID: uuid.NewString(), Username: in.Username, PasswordHash: hash, Role: in.Role}
	if err := s.db.Users.Create(u); err != nil {
		if err == database.ErrDuplicate {
			writeErr(w, http.StatusConflict, "username already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	u.PasswordHash = ""
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.db.Users.Get(id)
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}

	var in userInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" {
		in.Username = existing.Username
	}
	if in.Role == "" {
		in.Role = existing.Role
	}
	if !database.ValidRole(in.Role) {
		writeErr(w, http.StatusBadRequest, "role must be admin, operator or viewer")
		return
	}
	// Guard against demoting/locking out the last admin.
	if existing.Role == database.RoleAdmin && in.Role != database.RoleAdmin {
		if last, _ := s.isLastAdmin(id); last {
			writeErr(w, http.StatusBadRequest, "cannot demote the last admin")
			return
		}
	}

	var hash string
	if in.Password != "" {
		if len(in.Password) < 4 {
			writeErr(w, http.StatusBadRequest, "password must be at least 4 characters")
			return
		}
		if hash, err = hashPassword(in.Password); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.db.Users.Update(id, in.Username, in.Role, hash); err != nil {
		if err == database.ErrDuplicate {
			writeErr(w, http.StatusConflict, "username already exists")
			return
		}
		s.notFoundOr500(w, err)
		return
	}
	// A role or password change invalidates existing sessions.
	if in.Role != existing.Role || hash != "" {
		s.auth.revokeUser(id)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.db.Users.Get(id)
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}
	if existing.Role == database.RoleAdmin {
		if last, _ := s.isLastAdmin(id); last {
			writeErr(w, http.StatusBadRequest, "cannot delete the last admin")
			return
		}
	}
	if err := s.db.Users.Delete(id); err != nil {
		s.notFoundOr500(w, err)
		return
	}
	s.auth.revokeUser(id)
	w.WriteHeader(http.StatusNoContent)
}

// isLastAdmin reports whether userID is the only admin account.
func (s *Server) isLastAdmin(userID string) (bool, error) {
	users, err := s.db.Users.List()
	if err != nil {
		return false, err
	}
	admins := 0
	for _, u := range users {
		if u.Role == database.RoleAdmin && u.ID != userID {
			admins++
		}
	}
	return admins == 0, nil
}
