package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// The guards below all protect one thing: an installation must never end up
// with nobody who can administer it. There is no recovery path short of
// editing the database by hand, so these are refusals, not warnings.
var (
	errLastAdmin  = errors.New("this is the only administrator; promote another account first")
	errSelfDelete = errors.New("you cannot delete the account you are signed in as")
	errSelfDemote = errors.New("you cannot remove your own administrator access")
)

type userPayload struct {
	Username string      `json:"username"`
	Password string      `json:"password"`
	Role     domain.Role `json:"role"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.opts.Users.List(r.Context())
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var payload userPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, s.logger, err)
		return
	}

	username := domain.NormalizeUsername(payload.Username)
	if err := domain.ValidateCredentials(username, payload.Password); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if !payload.Role.Valid() {
		v := &domain.ValidationError{}
		v.Add("role", "must be admin or viewer")
		writeError(w, s.logger, v)
		return
	}

	hash, err := hashPasswordAt(payload.Password, s.passwordCost)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	user := &domain.User{Username: username, PasswordHash: hash, Role: payload.Role}
	if err := s.opts.Users.Create(r.Context(), user); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			v := &domain.ValidationError{}
			v.Add("username", "is already taken")
			writeError(w, s.logger, v)
			return
		}
		writeError(w, s.logger, err)
		return
	}

	s.logger.Info("user created", "username", user.Username, "role", user.Role)
	writeJSON(w, s.logger, http.StatusCreated, user)
}

type roleUpdatePayload struct {
	Role domain.Role `json:"role"`
}

func (s *Server) handleUpdateUserRole(w http.ResponseWriter, r *http.Request) {
	id, caller, err := s.userTarget(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	var payload roleUpdatePayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if !payload.Role.Valid() {
		v := &domain.ValidationError{}
		v.Add("role", "must be admin or viewer")
		writeError(w, s.logger, v)
		return
	}

	target, err := s.opts.Users.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	if target.Role == domain.RoleAdmin && payload.Role != domain.RoleAdmin {
		if id == caller.UserID {
			writeError(w, s.logger, errors.Join(errBadRequest, errSelfDemote))
			return
		}
		if err := s.refuseIfLastAdmin(r.Context()); err != nil {
			writeError(w, s.logger, err)
			return
		}
	}

	if err := s.opts.Users.UpdateRole(r.Context(), id, payload.Role); err != nil {
		writeError(w, s.logger, err)
		return
	}

	s.logger.Info("user role changed",
		"username", target.Username, "from", target.Role, "to", payload.Role)
	writeJSON(w, s.logger, http.StatusNoContent, nil)
}

type resetPasswordPayload struct {
	NewPassword string `json:"newPassword"`
}

// handleResetUserPassword lets an administrator set someone else's password
// without knowing the old one, which is the only way to recover an account.
// Changing your own still goes through /api/auth/password and still requires
// the current password.
func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	id, _, err := s.userTarget(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	var payload resetPasswordPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, s.logger, err)
		return
	}

	target, err := s.opts.Users.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := domain.ValidateCredentials(target.Username, payload.NewPassword); err != nil {
		writeError(w, s.logger, err)
		return
	}

	hash, err := hashPasswordAt(payload.NewPassword, s.passwordCost)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.Users.UpdatePassword(r.Context(), id, hash); err != nil {
		writeError(w, s.logger, err)
		return
	}

	s.logger.Info("password reset by an administrator", "username", target.Username)
	writeJSON(w, s.logger, http.StatusNoContent, nil)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, caller, err := s.userTarget(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	// Deleting yourself would sign you out mid-request and, if you were the
	// only admin, lock the installation.
	if id == caller.UserID {
		writeError(w, s.logger, errors.Join(errBadRequest, errSelfDelete))
		return
	}

	target, err := s.opts.Users.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if target.Role == domain.RoleAdmin {
		if err := s.refuseIfLastAdmin(r.Context()); err != nil {
			writeError(w, s.logger, err)
			return
		}
	}

	if err := s.opts.Users.Delete(r.Context(), id); err != nil {
		writeError(w, s.logger, err)
		return
	}

	s.logger.Info("user deleted", "username", target.Username)
	writeJSON(w, s.logger, http.StatusNoContent, nil)
}

// userTarget resolves the path id and the caller together, since every handler
// here needs both.
func (s *Server) userTarget(r *http.Request) (int64, principal, error) {
	id, err := pathID(r)
	if err != nil {
		return 0, principal{}, err
	}
	caller, ok := principalFrom(r.Context())
	if !ok {
		return 0, principal{}, errUnauthorized
	}
	return id, caller, nil
}

func (s *Server) refuseIfLastAdmin(ctx context.Context) error {
	admins, err := s.opts.Users.CountAdmins(ctx)
	if err != nil {
		return err
	}
	if admins <= 1 {
		return errors.Join(errBadRequest, errLastAdmin)
	}
	return nil
}
