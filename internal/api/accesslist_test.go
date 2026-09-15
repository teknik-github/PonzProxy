package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/ponzproxy/ponzproxy/internal/api"
	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/store"
)

// accessHarness drives the access-list endpoints directly. Authentication and
// the role check are the Server's middleware and are covered by its own
// tests; what matters here is what the handlers do with a payload.
type accessHarness struct {
	t       *testing.T
	mux     *http.ServeMux
	store   *store.Store
	applied int
}

func newAccessHarness(t *testing.T) *accessHarness {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	h := &accessHarness{t: t, store: db, mux: http.NewServeMux()}
	handlers := api.NewAccessListHandlers(api.AccessListOptions{
		Lists:       db.AccessLists(),
		ApplyConfig: func(context.Context) error { h.applied++; return nil },
		// bcrypt's minimum cost: these tests exercise the handlers, not the
		// hash, and the production cost would dominate the suite's runtime.
		PasswordCost: bcrypt.MinCost,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	h.mux.HandleFunc("GET /api/access-lists", handlers.HandleList)
	h.mux.HandleFunc("GET /api/access-lists/{id}", handlers.HandleGet)
	h.mux.HandleFunc("POST /api/access-lists", handlers.HandleCreate)
	h.mux.HandleFunc("PUT /api/access-lists/{id}", handlers.HandleUpdate)
	h.mux.HandleFunc("DELETE /api/access-lists/{id}", handlers.HandleDelete)
	return h
}

func (h *accessHarness) do(method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("encode request: %v", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req := httptest.NewRequest(method, path, payload)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, req)
	return w
}

func (h *accessHarness) decode(w *httptest.ResponseRecorder, v any) {
	h.t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		h.t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
}

// accessListResponse is the read shape as the UI sees it, hash excluded.
type accessListResponse struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	SatisfyAny bool   `json:"satisfyAny"`
	Rules      []struct {
		Action string `json:"action"`
		CIDR   string `json:"cidr"`
	} `json:"rules"`
	BasicAuth []struct {
		Username string `json:"username"`
	} `json:"basicAuth"`
	InUseByHosts int `json:"inUseByHosts"`
}

func validAccessList() map[string]any {
	return map[string]any{
		"name":       "office",
		"satisfyAny": false,
		"rules": []map[string]any{
			{"action": "allow", "cidr": "10.0.0.0/8"},
			{"action": "deny", "cidr": "10.4.0.0/16"},
		},
		"basicAuth": []map[string]any{},
	}
}

func TestCreateAccessList(t *testing.T) {
	h := newAccessHarness(t)

	w := h.do(http.MethodPost, "/api/access-lists", validAccessList())
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s, want 201", w.Code, w.Body.String())
	}
	var created accessListResponse
	h.decode(w, &created)
	if created.ID == 0 || len(created.Rules) != 2 {
		t.Fatalf("created = %+v, want an id and two rules", created)
	}
	if h.applied != 1 {
		t.Errorf("applied %d configurations, want 1: the proxy must learn about a new list", h.applied)
	}

	w = h.do(http.MethodGet, "/api/access-lists", nil)
	var listed []accessListResponse
	h.decode(w, &listed)
	if len(listed) != 1 || listed[0].InUseByHosts != 0 {
		t.Fatalf("list = %+v, want one unused list", listed)
	}
}

func TestCreateAccessListRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		field  string
	}{
		{
			name:   "malformed cidr",
			mutate: func(p map[string]any) { p["rules"] = []map[string]any{{"action": "allow", "cidr": "10.0.0.0/33"}} },
			field:  "rules[0].cidr",
		},
		{
			name:   "unknown action",
			mutate: func(p map[string]any) { p["rules"] = []map[string]any{{"action": "drop", "cidr": "10.0.0.0/8"}} },
			field:  "rules[0].action",
		},
		{
			name:   "no name",
			mutate: func(p map[string]any) { p["name"] = "" },
			field:  "name",
		},
		{
			name: "restricts nothing",
			mutate: func(p map[string]any) {
				p["rules"] = []map[string]any{}
				p["basicAuth"] = []map[string]any{}
			},
			field: "rules",
		},
		{
			name: "satisfy any with only address rules",
			mutate: func(p map[string]any) {
				p["satisfyAny"] = true
			},
			field: "satisfyAny",
		},
		{
			name: "new user without a password",
			mutate: func(p map[string]any) {
				p["basicAuth"] = []map[string]any{{"username": "alice"}}
			},
			field: "basicAuth[0].password",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newAccessHarness(t)
			payload := validAccessList()
			tc.mutate(payload)

			w := h.do(http.MethodPost, "/api/access-lists", payload)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("create = %d %s, want 422", w.Code, w.Body.String())
			}
			var body struct {
				Fields []domain.FieldError `json:"fields"`
			}
			h.decode(w, &body)
			for _, f := range body.Fields {
				if f.Field == tc.field {
					return
				}
			}
			t.Errorf("fields = %+v, want one naming %q", body.Fields, tc.field)
		})
	}
}

func TestAccessListBasicAuthPasswordHandling(t *testing.T) {
	h := newAccessHarness(t)
	ctx := context.Background()

	payload := validAccessList()
	payload["basicAuth"] = []map[string]any{{"username": "alice", "password": "opensesame"}}

	w := h.do(http.MethodPost, "/api/access-lists", payload)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s, want 201", w.Code, w.Body.String())
	}
	var created accessListResponse
	h.decode(w, &created)
	if bytes.Contains(w.Body.Bytes(), []byte("opensesame")) ||
		bytes.Contains(w.Body.Bytes(), []byte("$2a$")) {
		t.Fatalf("the response leaked credentials: %s", w.Body.String())
	}

	stored, err := h.store.AccessLists().Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored.BasicAuth) != 1 {
		t.Fatalf("stored users = %+v, want one", stored.BasicAuth)
	}
	hash := stored.BasicAuth[0].PasswordHash
	if hash == "opensesame" || hash == "" {
		t.Fatalf("stored password hash = %q, want a bcrypt hash", hash)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("opensesame")); err != nil {
		t.Fatalf("the stored hash does not match the password: %v", err)
	}

	// An edit that leaves the password field blank must keep the hash: the
	// UI never receives one, so it cannot send it back.
	payload["name"] = "office vpn"
	payload["basicAuth"] = []map[string]any{{"username": "alice"}}
	w = h.do(http.MethodPut, "/api/access-lists/"+strconv.FormatInt(created.ID, 10), payload)
	if w.Code != http.StatusOK {
		t.Fatalf("update = %d %s, want 200", w.Code, w.Body.String())
	}

	stored, err = h.store.AccessLists().Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("read back after update: %v", err)
	}
	if stored.Name != "office vpn" {
		t.Errorf("name = %q, want the edit to have landed", stored.Name)
	}
	if stored.BasicAuth[0].PasswordHash != hash {
		t.Error("a blank password field replaced the stored hash")
	}

	// A new password does replace it.
	payload["basicAuth"] = []map[string]any{{"username": "alice", "password": "another one"}}
	if w = h.do(http.MethodPut, "/api/access-lists/"+strconv.FormatInt(created.ID, 10), payload); w.Code != http.StatusOK {
		t.Fatalf("update = %d %s, want 200", w.Code, w.Body.String())
	}
	stored, err = h.store.AccessLists().Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("read back after password change: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword(
		[]byte(stored.BasicAuth[0].PasswordHash), []byte("another one")); err != nil {
		t.Fatalf("the new password was not stored: %v", err)
	}
}

func TestAccessListNormalisesOnWrite(t *testing.T) {
	h := newAccessHarness(t)

	payload := validAccessList()
	payload["rules"] = []map[string]any{
		{"action": "ALLOW", "cidr": " 10.1.2.3/16 "},
		{"action": "deny", "cidr": "203.0.113.7"},
	}

	w := h.do(http.MethodPost, "/api/access-lists", payload)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s, want 201", w.Code, w.Body.String())
	}
	var created accessListResponse
	h.decode(w, &created)
	if created.Rules[0].CIDR != "10.1.0.0/16" || created.Rules[0].Action != "allow" {
		t.Errorf("rule 0 = %+v, want it masked and lowercased", created.Rules[0])
	}
	if created.Rules[1].CIDR != "203.0.113.7/32" {
		t.Errorf("rule 1 = %+v, want a bare address widened to /32", created.Rules[1])
	}
}

func TestAccessListDeleteAndMissing(t *testing.T) {
	h := newAccessHarness(t)

	w := h.do(http.MethodPost, "/api/access-lists", validAccessList())
	var created accessListResponse
	h.decode(w, &created)

	if w = h.do(http.MethodGet, "/api/access-lists/"+strconv.FormatInt(created.ID, 10), nil); w.Code != http.StatusOK {
		t.Fatalf("get = %d %s, want 200", w.Code, w.Body.String())
	}
	if w = h.do(http.MethodDelete, "/api/access-lists/"+strconv.FormatInt(created.ID, 10), nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s, want 204", w.Code, w.Body.String())
	}
	if w = h.do(http.MethodGet, "/api/access-lists/"+strconv.FormatInt(created.ID, 10), nil); w.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", w.Code)
	}
	if w = h.do(http.MethodPut, "/api/access-lists/"+strconv.FormatInt(created.ID, 10), validAccessList()); w.Code != http.StatusNotFound {
		t.Fatalf("update after delete = %d, want 404", w.Code)
	}
}
