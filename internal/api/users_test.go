package api_test

import (
	"context"
	"net/http"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// addUser inserts an account directly, so a test can set up a second admin
// without going through the API it is about to exercise.
func (h *harness) addUser(username, password string, role domain.Role) *domain.User {
	h.t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), testPasswordCost)
	if err != nil {
		h.t.Fatal(err)
	}
	u := &domain.User{Username: username, PasswordHash: string(hash), Role: role}
	if err := h.store.Users().Create(context.Background(), u); err != nil {
		h.t.Fatalf("create user: %v", err)
	}
	return u
}

func TestCreateAndListUsers(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	w := h.do(http.MethodPost, "/api/users", map[string]any{
		"username": "ops", "password": "a long enough password", "role": "viewer",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}

	var created domain.User
	h.decode(w, &created)
	if created.Role != domain.RoleViewer {
		t.Errorf("role = %q, want viewer", created.Role)
	}
	// The hash must never reach a client, even an administrator's.
	if bodyHasHash(w.Body.String()) {
		t.Error("the response carried a password hash")
	}

	w = h.do(http.MethodGet, "/api/users", nil)
	var users []domain.User
	h.decode(w, &users)
	if len(users) != 2 {
		t.Fatalf("listed %d users, want admin plus the new one", len(users))
	}

	// The new account must actually be able to sign in.
	if got := h.login("ops", "a long enough password").Code; got != http.StatusOK {
		t.Errorf("new user login = %d, want 200", got)
	}
}

func bodyHasHash(body string) bool {
	return len(body) > 0 && (contains(body, "$2a$") || contains(body, "passwordHash"))
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}

func TestCreateUserRejectsBadInput(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	cases := []struct {
		name    string
		payload map[string]any
		status  int
	}{
		{"short password", map[string]any{"username": "ops", "password": "short", "role": "viewer"}, http.StatusUnprocessableEntity},
		{"bad username", map[string]any{"username": "Ops!", "password": "a long enough password", "role": "viewer"}, http.StatusUnprocessableEntity},
		{"unknown role", map[string]any{"username": "ops", "password": "a long enough password", "role": "wizard"}, http.StatusUnprocessableEntity},
		{"duplicate", map[string]any{"username": "admin", "password": "a long enough password", "role": "admin"}, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.do(http.MethodPost, "/api/users", tc.payload).Code; got != tc.status {
				t.Errorf("status = %d, want %d", got, tc.status)
			}
		})
	}
}

// TestCannotLockYourselfOut is the reason these guards exist: there is no
// recovery path short of editing the database by hand.
func TestCannotLockYourselfOut(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	var me domain.User
	h.decode(h.do(http.MethodGet, "/api/auth/me", nil), &me)

	t.Run("delete yourself", func(t *testing.T) {
		if got := h.do(http.MethodDelete, "/api/users/"+itoa(me.ID), nil).Code; got != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", got)
		}
	})

	t.Run("demote yourself", func(t *testing.T) {
		w := h.do(http.MethodPut, "/api/users/"+itoa(me.ID)+"/role",
			map[string]any{"role": "viewer"})
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("delete the only other admin is allowed once a second exists", func(t *testing.T) {
		other := h.addUser("second", "a long enough password", domain.RoleAdmin)
		if got := h.do(http.MethodDelete, "/api/users/"+itoa(other.ID), nil).Code; got != http.StatusNoContent {
			t.Errorf("status = %d, want 204 — two admins existed", got)
		}
	})
}

func TestCannotRemoveTheLastAdmin(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	// Sign in as a second admin so the guard is about the target, not about
	// the caller deleting themselves.
	second := h.addUser("second", "a long enough password", domain.RoleAdmin)
	w := h.login("second", "a long enough password")
	var resp struct {
		Token string `json:"token"`
	}
	h.decode(w, &resp)
	h.token = resp.Token

	var admin domain.User
	for _, u := range listUsers(t, h) {
		if u.Username == "admin" {
			admin = u
		}
	}

	// Two admins: removing one is fine.
	if got := h.do(http.MethodDelete, "/api/users/"+itoa(admin.ID), nil).Code; got != http.StatusNoContent {
		t.Fatalf("deleting one of two admins = %d, want 204", got)
	}
	// One admin left, and it is the caller — both guards should fire.
	if got := h.do(http.MethodDelete, "/api/users/"+itoa(second.ID), nil).Code; got != http.StatusBadRequest {
		t.Errorf("deleting the last admin = %d, want 400", got)
	}
}

func listUsers(t *testing.T, h *harness) []domain.User {
	t.Helper()
	var users []domain.User
	h.decode(h.do(http.MethodGet, "/api/users", nil), &users)
	return users
}

func TestAdminCanResetAnotherPassword(t *testing.T) {
	h := newHarness(t)
	h.authenticate()
	other := h.addUser("ops", "the original password", domain.RoleViewer)

	w := h.do(http.MethodPost, "/api/users/"+itoa(other.ID)+"/password",
		map[string]any{"newPassword": "a brand new password"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("reset = %d %s", w.Code, w.Body.String())
	}

	if got := h.login("ops", "a brand new password").Code; got != http.StatusOK {
		t.Errorf("login with the new password = %d, want 200", got)
	}
	if got := h.login("ops", "the original password").Code; got != http.StatusUnauthorized {
		t.Errorf("login with the old password = %d, want 401", got)
	}
}

func TestViewersCannotManageUsers(t *testing.T) {
	h := newHarness(t)
	h.authenticate()
	viewer := h.addUser("watcher", "a long enough password", domain.RoleViewer)

	w := h.login("watcher", "a long enough password")
	var resp struct {
		Token string `json:"token"`
	}
	h.decode(w, &resp)
	h.token = resp.Token

	// A viewer may see who else has access, but change nothing.
	if got := h.do(http.MethodGet, "/api/users", nil).Code; got != http.StatusOK {
		t.Errorf("viewer list = %d, want 200", got)
	}
	for _, tc := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPost, "/api/users", map[string]any{"username": "x", "password": "a long enough password", "role": "admin"}},
		{http.MethodPut, "/api/users/" + itoa(viewer.ID) + "/role", map[string]any{"role": "admin"}},
		{http.MethodPost, "/api/users/" + itoa(viewer.ID) + "/password", map[string]any{"newPassword": "a long enough password"}},
		{http.MethodDelete, "/api/users/" + itoa(viewer.ID), nil},
	} {
		if got := h.do(tc.method, tc.path, tc.body).Code; got != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", tc.method, tc.path, got)
		}
	}
}
