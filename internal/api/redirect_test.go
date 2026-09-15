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

	"github.com/ponzproxy/ponzproxy/internal/api"
	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/store"
)

// redirectHarness exercises the redirect endpoints against a real store.
//
// It mounts them on bare muxes rather than going through NewServer because
// RedirectHandlers takes its own collaborators; the session and admin checks
// are applied by whoever calls Register, and are covered by the server's own
// tests, so what is left to test here is the handler behaviour.
type redirectHarness struct {
	t       *testing.T
	mux     *http.ServeMux
	store   *store.Store
	applied int
}

func newRedirectHarness(t *testing.T) *redirectHarness {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "redirects.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	h := &redirectHarness{t: t, mux: http.NewServeMux(), store: db}
	api.NewRedirectHandlers(api.RedirectOptions{
		Redirects:   db.Redirects(),
		Certs:       db.Certificates(),
		ApplyConfig: func(context.Context) error { h.applied++; return nil },
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Register(h.mux, h.mux)
	return h
}

func (h *redirectHarness) do(method, path string, body any) *httptest.ResponseRecorder {
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

func (h *redirectHarness) decode(w *httptest.ResponseRecorder, v any) {
	h.t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		h.t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
}

// fieldErrors collects the per-field messages from a 422 body.
func (h *redirectHarness) fieldErrors(w *httptest.ResponseRecorder) map[string]string {
	h.t.Helper()
	var body struct {
		Fields []domain.FieldError `json:"fields"`
	}
	h.decode(w, &body)
	out := make(map[string]string, len(body.Fields))
	for _, f := range body.Fields {
		out[f.Field] = f.Message
	}
	return out
}

func validRedirect() map[string]any {
	return map[string]any{
		"name":         "old site",
		"enabled":      true,
		"domains":      []string{"old.example.com"},
		"target":       "https://new.example.net",
		"statusCode":   308,
		"preservePath": true,
	}
}

func TestRedirectLifecycleAppliesConfiguration(t *testing.T) {
	h := newRedirectHarness(t)

	w := h.do(http.MethodPost, "/api/redirects", validRedirect())
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created domain.Redirect
	h.decode(w, &created)
	if created.ID == 0 {
		t.Fatal("the created redirect has no id")
	}
	if h.applied != 1 {
		t.Errorf("configuration applied %d times after create, want 1", h.applied)
	}

	payload := validRedirect()
	payload["statusCode"] = 301
	payload["preservePath"] = false
	w = h.do(http.MethodPut, "/api/redirects/"+strconv.FormatInt(created.ID, 10), payload)
	if w.Code != http.StatusOK {
		t.Fatalf("update = %d %s", w.Code, w.Body.String())
	}
	var updated domain.Redirect
	h.decode(w, &updated)
	if updated.StatusCode != 301 || updated.PreservePath {
		t.Errorf("update did not take: %+v", updated)
	}
	if h.applied != 2 {
		t.Errorf("configuration applied %d times after update, want 2", h.applied)
	}

	if w := h.do(http.MethodDelete, "/api/redirects/"+strconv.FormatInt(created.ID, 10), nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", w.Code, w.Body.String())
	}
	if h.applied != 3 {
		t.Errorf("configuration applied %d times after delete, want 3", h.applied)
	}
	if w := h.do(http.MethodGet, "/api/redirects/"+strconv.FormatInt(created.ID, 10), nil); w.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", w.Code)
	}
}

func TestRedirectCreateNormalisesTheTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		want   string
	}{
		{"scheme supplied", "https://new.example.net/app", "https://new.example.net/app"},
		{"scheme missing", "new.example.net/app", "https://new.example.net/app"},
		{"mixed case host", "New.Example.NET", "https://new.example.net"},
		{"plaintext target is respected", "http://new.example.net", "http://new.example.net"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRedirectHarness(t)
			payload := validRedirect()
			payload["target"] = tc.target

			w := h.do(http.MethodPost, "/api/redirects", payload)
			if w.Code != http.StatusCreated {
				t.Fatalf("create = %d %s", w.Code, w.Body.String())
			}
			var created domain.Redirect
			h.decode(w, &created)
			if created.Target != tc.want {
				t.Errorf("target = %q, want %q", created.Target, tc.want)
			}
		})
	}
}

func TestRedirectAcceptsEveryAllowedStatusAndRefusesTheRest(t *testing.T) {
	for _, code := range domain.RedirectStatuses() {
		h := newRedirectHarness(t)
		payload := validRedirect()
		payload["statusCode"] = code

		if w := h.do(http.MethodPost, "/api/redirects", payload); w.Code != http.StatusCreated {
			t.Errorf("status %d = %d %s, want 201", code, w.Code, w.Body.String())
		}
	}

	for _, code := range []int{200, 303, 304, 400, 999} {
		h := newRedirectHarness(t)
		payload := validRedirect()
		payload["statusCode"] = code

		w := h.do(http.MethodPost, "/api/redirects", payload)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status %d = %d %s, want 422", code, w.Code, w.Body.String())
		}
		if _, ok := h.fieldErrors(w)["statusCode"]; !ok {
			t.Errorf("status %d: the response does not name the statusCode field: %s",
				code, w.Body.String())
		}
	}
}

func TestRedirectRejectsANonHTTPTarget(t *testing.T) {
	h := newRedirectHarness(t)
	payload := validRedirect()
	payload["target"] = "javascript:alert(document.cookie)"

	w := h.do(http.MethodPost, "/api/redirects", payload)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create = %d %s, want 422", w.Code, w.Body.String())
	}
	if _, ok := h.fieldErrors(w)["target"]; !ok {
		t.Errorf("the response does not name the target field: %s", w.Body.String())
	}
}

func TestRedirectRejectsADomainAHostAlreadyRoutes(t *testing.T) {
	h := newRedirectHarness(t)

	host := &domain.Host{
		Name:      "api",
		Enabled:   true,
		Domains:   []string{"shared.example.com"},
		Algorithm: domain.RoundRobin,
		Upstreams: []domain.Upstream{
			{Scheme: "http", Address: "10.0.0.1:8080", Weight: 1, Enabled: true},
		},
	}
	host.Normalize()
	if err := h.store.Hosts().Create(context.Background(), host); err != nil {
		t.Fatalf("create host: %v", err)
	}

	payload := validRedirect()
	payload["domains"] = []string{"shared.example.com"}
	w := h.do(http.MethodPost, "/api/redirects", payload)
	if w.Code != http.StatusConflict {
		t.Fatalf("create = %d %s, want 409", w.Code, w.Body.String())
	}
	if h.applied != 0 {
		t.Errorf("a rejected create still applied configuration %d times", h.applied)
	}
}

func TestRedirectRejectsAnUnknownCertificate(t *testing.T) {
	h := newRedirectHarness(t)
	payload := validRedirect()
	payload["certificateId"] = 999

	w := h.do(http.MethodPost, "/api/redirects", payload)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create = %d %s, want 422", w.Code, w.Body.String())
	}
	if _, ok := h.fieldErrors(w)["certificateId"]; !ok {
		t.Errorf("the response does not name the certificateId field: %s", w.Body.String())
	}
}

func TestRedirectRejectsUnknownFields(t *testing.T) {
	h := newRedirectHarness(t)
	payload := validRedirect()
	// A typo must fail loudly rather than being silently ignored.
	payload["preservePaths"] = true

	if w := h.do(http.MethodPost, "/api/redirects", payload); w.Code != http.StatusBadRequest {
		t.Errorf("create with an unknown field = %d %s, want 400", w.Code, w.Body.String())
	}
}

func TestRedirectListAndGet(t *testing.T) {
	h := newRedirectHarness(t)

	w := h.do(http.MethodPost, "/api/redirects", validRedirect())
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created domain.Redirect
	h.decode(w, &created)

	w = h.do(http.MethodGet, "/api/redirects", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	var list []domain.Redirect
	h.decode(w, &list)
	if len(list) != 1 || list[0].Domains[0] != "old.example.com" {
		t.Fatalf("list = %+v, want the one redirect with its domains", list)
	}

	w = h.do(http.MethodGet, "/api/redirects/"+strconv.FormatInt(created.ID, 10), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get = %d %s", w.Code, w.Body.String())
	}
	var got domain.Redirect
	h.decode(w, &got)
	if got.ID != created.ID {
		t.Errorf("get returned id %d, want %d", got.ID, created.ID)
	}

	if w := h.do(http.MethodGet, "/api/redirects/nonsense", nil); w.Code != http.StatusBadRequest {
		t.Errorf("get with a non-numeric id = %d, want 400", w.Code)
	}
}

// The UI builds its status picker from this list rather than keeping a second
// copy of the allowed set.
func TestRedirectStatusesEndpoint(t *testing.T) {
	h := newRedirectHarness(t)

	w := h.do(http.MethodGet, "/api/redirect-statuses", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list statuses = %d %s", w.Code, w.Body.String())
	}
	var entries []struct {
		Value       int    `json:"value"`
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	h.decode(w, &entries)

	if len(entries) != len(domain.RedirectStatuses()) {
		t.Fatalf("got %d statuses, want %d", len(entries), len(domain.RedirectStatuses()))
	}
	for i, code := range domain.RedirectStatuses() {
		if entries[i].Value != code {
			t.Errorf("entry %d is %d, want %d", i, entries[i].Value, code)
		}
		if entries[i].Label == "" || entries[i].Description == "" {
			t.Errorf("entry %d has nothing for the operator to read: %+v", i, entries[i])
		}
	}
}
