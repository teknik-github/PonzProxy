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

	"golang.org/x/crypto/bcrypt"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/api"
	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/metrics"
	"github.com/ponzproxy/ponzproxy/internal/store"
)

// stubCertService stands in for certmgr: these tests cover the control plane,
// not ACME.
type stubCertService struct {
	issued  []int64
	reloads int
}

func (s *stubCertService) Issue(_ context.Context, id int64) error {
	s.issued = append(s.issued, id)
	return nil
}

func (s *stubCertService) Reload(context.Context) error {
	s.reloads++
	return nil
}

type harness struct {
	t       *testing.T
	server  *api.Server
	store   *store.Store
	certs   *stubCertService
	applied int
	token   string
}

const (
	testPassword     = "correct horse battery"
	testPasswordCost = bcrypt.MinCost
)

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// bcrypt's minimum cost: these tests exercise the control plane, not
	// the hash, and the production cost would dominate the suite's runtime.
	if _, err := api.EnsureBootstrapUser(ctx, db.Users(), testPassword, testPasswordCost); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &harness{t: t, store: db, certs: &stubCertService{}}
	h.server = api.NewServer(ctx, api.Options{
		Hosts:        db.Hosts(),
		Certs:        db.Certificates(),
		Users:        db.Users(),
		Metrics:      db.Metrics(),
		AccessLists:  db.AccessLists(),
		Redirects:    db.Redirects(),
		CertManager:  h.certs,
		Collector:    metrics.New(db.Metrics(), logger),
		JWTSecret:    []byte("test-secret-value-for-signing-only"),
		SessionTTL:   time.Hour,
		PasswordCost: testPasswordCost,
		ApplyConfig:  func(context.Context) error { h.applied++; return nil },
		Logger:       logger,
	})
	return h
}

// do sends a request, attaching the session token when one has been obtained.
func (h *harness) do(method, path string, body any) *httptest.ResponseRecorder {
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
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	w := httptest.NewRecorder()
	h.server.ServeHTTP(w, req)
	return w
}

func (h *harness) login(username, password string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/auth/login",
		map[string]string{"username": username, "password": password})
}

func (h *harness) authenticate() {
	h.t.Helper()
	w := h.login("admin", testPassword)
	if w.Code != http.StatusOK {
		h.t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	h.decode(w, &resp)
	h.token = resp.Token
}

func (h *harness) decode(w *httptest.ResponseRecorder, v any) {
	h.t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		h.t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
}

func validHost() map[string]any {
	return map[string]any{
		"name":      "api",
		"enabled":   true,
		"domains":   []string{"api.example.com"},
		"algorithm": "round_robin",
		"upstreams": []map[string]any{
			{"scheme": "http", "address": "10.0.0.1:8080", "weight": 1, "enabled": true},
		},
		"healthCheck": map[string]any{
			"enabled": true, "path": "/", "intervalSeconds": 10, "timeoutSeconds": 5,
			"healthyThreshold": 2, "unhealthyThreshold": 3,
		},
	}
}

func TestEndpointsRequireAuthentication(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/hosts"},
		{http.MethodPost, "/api/hosts"},
		{http.MethodGet, "/api/certificates"},
		{http.MethodGet, "/api/metrics/live"},
		{http.MethodGet, "/api/auth/me"},
	} {
		if got := h.do(tc.method, tc.path, nil).Code; got != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", tc.method, tc.path, got)
		}
	}

	// Health is the one endpoint that must answer without a session, since
	// a load balancer probe has no credentials.
	if got := h.do(http.MethodGet, "/api/health", nil).Code; got != http.StatusOK {
		t.Errorf("health check = %d, want 200", got)
	}
}

func TestLoginRejectsWrongCredentials(t *testing.T) {
	h := newHarness(t)

	if got := h.login("admin", "wrong password").Code; got != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", got)
	}
	if got := h.login("nobody", testPassword).Code; got != http.StatusUnauthorized {
		t.Errorf("unknown user = %d, want 401", got)
	}

	// The two failures must be indistinguishable, or the response tells an
	// attacker which usernames exist.
	wrongPass := h.login("admin", "wrong password").Body.String()
	unknown := h.login("nobody", testPassword).Body.String()
	if wrongPass != unknown {
		t.Errorf("the two failures differ:\n  %s\n  %s", wrongPass, unknown)
	}
}

func TestTokenIsRejectedWhenTampered(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	valid := h.token
	if got := h.do(http.MethodGet, "/api/auth/me", nil).Code; got != http.StatusOK {
		t.Fatalf("a valid token was rejected: %d", got)
	}

	// Flip a character in the signature.
	h.token = valid[:len(valid)-2] + "xy"
	if got := h.do(http.MethodGet, "/api/auth/me", nil).Code; got != http.StatusUnauthorized {
		t.Errorf("a tampered token = %d, want 401", got)
	}

	// An unsigned token must never be accepted.
	h.token = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." +
		"eyJzdWIiOiIxIiwicm9sZSI6ImFkbWluIiwiaXNzIjoicG9uenByb3h5In0."
	if got := h.do(http.MethodGet, "/api/auth/me", nil).Code; got != http.StatusUnauthorized {
		t.Errorf("an alg=none token = %d, want 401", got)
	}
}

func TestHostLifecycleAppliesConfiguration(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	w := h.do(http.MethodPost, "/api/hosts", validHost())
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created domain.Host
	h.decode(w, &created)
	if created.ID == 0 {
		t.Fatal("the created host has no id")
	}
	if h.applied != 1 {
		t.Errorf("configuration applied %d times after create, want 1", h.applied)
	}

	payload := validHost()
	payload["algorithm"] = "least_connections"
	w = h.do(http.MethodPut, "/api/hosts/"+itoa(created.ID), payload)
	if w.Code != http.StatusOK {
		t.Fatalf("update = %d %s", w.Code, w.Body.String())
	}
	var updated domain.Host
	h.decode(w, &updated)
	if updated.Algorithm != domain.LeastConnections {
		t.Errorf("algorithm = %q, want least_connections", updated.Algorithm)
	}
	if h.applied != 2 {
		t.Errorf("configuration applied %d times after update, want 2", h.applied)
	}

	if w := h.do(http.MethodDelete, "/api/hosts/"+itoa(created.ID), nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", w.Code, w.Body.String())
	}
	if h.applied != 3 {
		t.Errorf("configuration applied %d times after delete, want 3", h.applied)
	}
	if w := h.do(http.MethodGet, "/api/hosts/"+itoa(created.ID), nil); w.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", w.Code)
	}
}

func TestValidationFailureNamesTheFields(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	bad := validHost()
	bad["name"] = ""
	bad["domains"] = []string{"not a domain"}
	bad["upstreams"] = []map[string]any{
		{"scheme": "ftp", "address": "nope", "weight": 0, "enabled": true},
	}

	w := h.do(http.MethodPost, "/api/hosts", bad)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", w.Code)
	}

	var body struct {
		Fields []domain.FieldError `json:"fields"`
	}
	h.decode(w, &body)

	// Every problem must come back at once, not one per round trip.
	named := map[string]bool{}
	for _, f := range body.Fields {
		named[f.Field] = true
	}
	for _, want := range []string{"name", "domains[0]", "upstreams[0].scheme", "upstreams[0].address"} {
		if !named[want] {
			t.Errorf("field %q was not reported; got %v", want, body.Fields)
		}
	}
	if h.applied != 0 {
		t.Error("an invalid host was applied to the proxy")
	}
}

func TestDuplicateDomainIsAConflict(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	if w := h.do(http.MethodPost, "/api/hosts", validHost()); w.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", w.Code, w.Body.String())
	}

	second := validHost()
	second["name"] = "another"
	w := h.do(http.MethodPost, "/api/hosts", second)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate domain = %d %s, want 409", w.Code, w.Body.String())
	}
}

func TestUnknownFieldIsRejected(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	payload := validHost()
	payload["algorythm"] = "round_robin" // a plausible typo

	if got := h.do(http.MethodPost, "/api/hosts", payload).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 so a misspelled field is not silently ignored", got)
	}
}

func TestViewerCannotChangeConfiguration(t *testing.T) {
	h := newHarness(t)

	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	viewer := &domain.User{Username: "watcher", PasswordHash: string(hash), Role: domain.RoleViewer}
	if err := h.store.Users().Create(context.Background(), viewer); err != nil {
		t.Fatal(err)
	}

	w := h.login("watcher", testPassword)
	if w.Code != http.StatusOK {
		t.Fatalf("viewer login = %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	h.decode(w, &resp)
	h.token = resp.Token

	if got := h.do(http.MethodGet, "/api/hosts", nil).Code; got != http.StatusOK {
		t.Errorf("viewer read = %d, want 200", got)
	}
	if got := h.do(http.MethodPost, "/api/hosts", validHost()).Code; got != http.StatusForbidden {
		t.Errorf("viewer write = %d, want 403", got)
	}
	if got := h.do(http.MethodDelete, "/api/hosts/1", nil).Code; got != http.StatusForbidden {
		t.Errorf("viewer delete = %d, want 403", got)
	}
}

func TestSelfSignedCertificateIsGeneratedOnCreate(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	w := h.do(http.MethodPost, "/api/certificates", map[string]any{
		"name": "internal", "source": "self_signed", "domains": []string{"box.internal"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}

	var view struct {
		ID             int64  `json:"id"`
		Installed      bool   `json:"installed"`
		ExpiresInDays  int    `json:"expiresInDays"`
		CertificatePEM string `json:"certificatePem"`
		PrivateKeyPEM  string `json:"privateKeyPem"`
	}
	h.decode(w, &view)

	if !view.Installed {
		t.Error("the certificate was not marked installed")
	}
	if view.ExpiresInDays < 300 {
		t.Errorf("expires in %d days, want roughly a year", view.ExpiresInDays)
	}
	// Key material must never be returned to a client.
	if view.PrivateKeyPEM != "" || view.CertificatePEM != "" {
		t.Error("the response carried key material")
	}
	if h.certs.reloads == 0 {
		t.Error("the certificate manager was not reloaded")
	}
}

func TestCertificateInUseCannotBeDeleted(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	w := h.do(http.MethodPost, "/api/certificates", map[string]any{
		"name": "api", "source": "self_signed", "domains": []string{"api.example.com"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create certificate = %d %s", w.Code, w.Body.String())
	}
	var cert struct {
		ID int64 `json:"id"`
	}
	h.decode(w, &cert)

	host := validHost()
	host["certificateId"] = cert.ID
	host["forceHttps"] = true
	if w := h.do(http.MethodPost, "/api/hosts", host); w.Code != http.StatusCreated {
		t.Fatalf("create host = %d %s", w.Code, w.Body.String())
	}

	if got := h.do(http.MethodDelete, "/api/certificates/"+itoa(cert.ID), nil).Code; got != http.StatusConflict {
		t.Errorf("delete in-use certificate = %d, want 409", got)
	}

	// It must also be reported as in use so the UI can explain why.
	w = h.do(http.MethodGet, "/api/certificates", nil)
	var certs []struct {
		ID           int64 `json:"id"`
		InUseByHosts int   `json:"inUseByHosts"`
	}
	h.decode(w, &certs)
	if len(certs) != 1 || certs[0].InUseByHosts != 1 {
		t.Errorf("certificate list = %+v, want one certificate in use by one host", certs)
	}
}

func TestHostRejectsAMissingCertificate(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	host := validHost()
	host["certificateId"] = 999

	w := h.do(http.MethodPost, "/api/hosts", host)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d %s, want 422", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("certificateId")) {
		t.Errorf("the error does not name the field: %s", w.Body.String())
	}
}

func TestACMECertificateRejectsWildcardWithoutDNS01(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	w := h.do(http.MethodPost, "/api/certificates", map[string]any{
		"name": "wild", "source": "acme",
		"domains": []string{"*.example.com"}, "challenge": "http-01",
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d %s, want 422", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("dns-01")) {
		t.Errorf("the error does not explain the dns-01 requirement: %s", w.Body.String())
	}
}

func TestMetricsHistoryRejectsAnOversizedWindow(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	// Ten years at minute resolution is millions of buckets.
	from := time.Now().Add(-10 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	path := "/api/metrics/history?resolution=minute&from=" + from
	if got := h.do(http.MethodGet, path, nil).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", got)
	}

	// The same window at day resolution is fine.
	path = "/api/metrics/history?resolution=day&from=" + from
	if got := h.do(http.MethodGet, path, nil).Code; got != http.StatusOK {
		t.Errorf("day resolution status = %d, want 200", got)
	}
}

func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	h := newHarness(t)
	h.authenticate()

	w := h.do(http.MethodPost, "/api/auth/password", map[string]string{
		"currentPassword": "not it", "newPassword": "a new long password",
	})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong current password = %d, want 401", w.Code)
	}

	w = h.do(http.MethodPost, "/api/auth/password", map[string]string{
		"currentPassword": testPassword, "newPassword": "short",
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("too-short password = %d, want 422", w.Code)
	}

	w = h.do(http.MethodPost, "/api/auth/password", map[string]string{
		"currentPassword": testPassword, "newPassword": "a new long password",
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("change = %d %s", w.Code, w.Body.String())
	}
	if got := h.login("admin", "a new long password").Code; got != http.StatusOK {
		t.Errorf("login with the new password = %d, want 200", got)
	}
	if got := h.login("admin", testPassword).Code; got != http.StatusUnauthorized {
		t.Errorf("login with the old password = %d, want 401", got)
	}
}

func itoa(v int64) string {
	return string(json.RawMessage(bytesFromInt(v)))
}

func bytesFromInt(v int64) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestLoginIsRateLimitedPerSource(t *testing.T) {
	h := newHarness(t)

	// Exhaust the burst from one address.
	for i := range 6 {
		w := h.login("admin", "wrong password")
		if w.Code == http.StatusTooManyRequests {
			if i < 4 {
				t.Fatalf("throttled after only %d attempts, too aggressive for a typo", i+1)
			}
			break
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, w.Code)
		}
	}

	w := h.login("admin", "wrong password")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d after the burst, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("a 429 without Retry-After leaves a client guessing")
	}

	// The correct password is refused too while the block holds: otherwise
	// the limit would not slow a guess down at all.
	if got := h.login("admin", testPassword).Code; got != http.StatusTooManyRequests {
		t.Errorf("status = %d for a valid password while blocked, want 429", got)
	}
}

func TestSuccessfulLoginKeepsTheBudgetClear(t *testing.T) {
	h := newHarness(t)

	// Mistype, then get it right, repeatedly. This must never lock out.
	for range 6 {
		if got := h.login("admin", "wrong password").Code; got != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 — a success should have cleared the budget", got)
		}
		if got := h.login("admin", testPassword).Code; got != http.StatusOK {
			t.Fatalf("status = %d for the correct password, want 200", got)
		}
	}
}
