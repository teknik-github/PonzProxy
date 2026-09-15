package proxy

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/metrics"
)

// engineWith builds an engine around a whole Config, which the redirect and
// access-list paths need and testEngine (hosts only) cannot express.
func engineWith(t *testing.T, cfg Config) *Engine {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(Options{
		Logger:     logger,
		Collector:  metrics.New(nopMetricsRepo{}, logger),
		MaxRetries: 2,
	})
	t.Cleanup(e.Close)
	e.Reload(cfg)
	return e
}

func request(t *testing.T, e *Engine, host, target string, tune ...func(*http.Request)) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+target, nil)
	req.Host = host
	req.RemoteAddr = "203.0.113.9:5555"
	for _, f := range tune {
		f(req)
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w.Result()
}

// ------------------------------------------------------------- redirects ---

func TestRedirectIsAnsweredBeforeHostLookup(t *testing.T) {
	rd := domain.Redirect{
		ID: 1, Name: "old site", Enabled: true,
		Domains: []string{"old.example.com"}, Target: "https://new.example.com",
		StatusCode: http.StatusPermanentRedirect, PreservePath: true,
	}
	rd.Normalize()
	if err := rd.Validate(); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}

	e := engineWith(t, Config{Redirects: []domain.Redirect{rd}})

	resp := request(t, e, "old.example.com", "/docs/page?q=1")
	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308", resp.StatusCode)
	}
	got := resp.Header.Get("Location")
	if !strings.HasPrefix(got, "https://new.example.com/docs/page") {
		t.Errorf("Location = %q, want the path preserved on the new host", got)
	}
	if !strings.Contains(got, "q=1") {
		t.Errorf("Location = %q, want the query preserved", got)
	}
}

func TestRedirectWithoutPathPreservation(t *testing.T) {
	rd := domain.Redirect{
		ID: 1, Name: "old site", Enabled: true,
		Domains: []string{"old.example.com"}, Target: "https://new.example.com/welcome",
		StatusCode: http.StatusFound, PreservePath: false,
	}
	rd.Normalize()
	e := engineWith(t, Config{Redirects: []domain.Redirect{rd}})

	resp := request(t, e, "old.example.com", "/anything/at/all")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "https://new.example.com/welcome" {
		t.Errorf("Location = %q, want the bare target", got)
	}
}

func TestDisabledRedirectIsNotRouted(t *testing.T) {
	rd := domain.Redirect{
		ID: 1, Name: "old site", Enabled: false,
		Domains: []string{"old.example.com"}, Target: "https://new.example.com",
		StatusCode: http.StatusMovedPermanently,
	}
	rd.Normalize()
	e := engineWith(t, Config{Redirects: []domain.Redirect{rd}})

	if got := request(t, e, "old.example.com", "/").StatusCode; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a disabled redirect", got)
	}
}

// ---------------------------------------------------------- access lists ---

func hostWithAccess(t *testing.T, list *domain.AccessList) Config {
	t.Helper()
	backend := backendServer(t, "a")

	h := hostFor("api.example.com", domain.RoundRobin, backend)
	id := list.ID
	h.AccessListID = &id

	return Config{
		Hosts:       []domain.Host{h},
		AccessLists: map[int64]*domain.AccessList{list.ID: list},
	}
}

func TestAccessListRefusesADeniedAddress(t *testing.T) {
	list := &domain.AccessList{
		ID: 1, Name: "office only",
		Rules: []domain.AccessRule{{Action: domain.AccessDeny, CIDR: "203.0.113.0/24"}},
	}
	list.Normalize()
	e := engineWith(t, hostWithAccess(t, list))

	resp := request(t, e, "api.example.com", "/")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a denied address", resp.StatusCode)
	}
	// A flat refusal, never a login prompt: credentials cannot help here.
	if got := resp.Header.Get("WWW-Authenticate"); got != "" {
		t.Errorf("WWW-Authenticate = %q, want none for an address refusal", got)
	}
}

func TestAccessListAllowsAPermittedAddress(t *testing.T) {
	list := &domain.AccessList{
		ID: 1, Name: "office only",
		Rules: []domain.AccessRule{{Action: domain.AccessAllow, CIDR: "203.0.113.0/24"}},
	}
	list.Normalize()
	e := engineWith(t, hostWithAccess(t, list))

	if got := request(t, e, "api.example.com", "/").StatusCode; got != http.StatusOK {
		t.Errorf("status = %d, want 200 for an allowed address", got)
	}
}

func TestAccessListChallengesForBasicAuth(t *testing.T) {
	hash, err := bcryptHash("hunter2hunter2")
	if err != nil {
		t.Fatal(err)
	}
	list := &domain.AccessList{
		ID: 1, Name: "staff",
		BasicAuth: []domain.BasicAuthUser{{Username: "ops", PasswordHash: hash}},
	}
	list.Normalize()
	e := engineWith(t, hostWithAccess(t, list))

	resp := request(t, e, "api.example.com", "/")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when credentials are required", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic ") {
		t.Errorf("WWW-Authenticate = %q, want a Basic challenge", got)
	}

	withCreds := func(user, pass string) func(*http.Request) {
		return func(r *http.Request) {
			r.Header.Set("Authorization", "Basic "+
				base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
		}
	}

	if got := request(t, e, "api.example.com", "/", withCreds("ops", "wrong")).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("status = %d for a wrong password, want 401", got)
	}
	if got := request(t, e, "api.example.com", "/", withCreds("ops", "hunter2hunter2")).StatusCode; got != http.StatusOK {
		t.Errorf("status = %d for correct credentials, want 200", got)
	}
}

// TestAccessRefusalNeverReachesAnUpstream is the point of doing this before
// upstream selection: a refused request must not touch a backend at all.
func TestAccessRefusalNeverReachesAnUpstream(t *testing.T) {
	var reached int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	list := &domain.AccessList{
		ID: 1, Name: "closed",
		Rules: []domain.AccessRule{{Action: domain.AccessDeny, CIDR: "0.0.0.0/0"}},
	}
	list.Normalize()

	h := hostFor("api.example.com", domain.RoundRobin)
	h.Upstreams = []domain.Upstream{{
		ID: 1, HostID: 1, Scheme: "http",
		Address: strings.TrimPrefix(backend.URL, "http://"), Weight: 1, Enabled: true,
	}}
	id := list.ID
	h.AccessListID = &id

	e := engineWith(t, Config{
		Hosts:       []domain.Host{h},
		AccessLists: map[int64]*domain.AccessList{list.ID: list},
	})

	for range 5 {
		if got := request(t, e, "api.example.com", "/").StatusCode; got != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", got)
		}
	}
	if reached != 0 {
		t.Errorf("the upstream was reached %d times by refused requests", reached)
	}
}

func TestHostWithoutAnAccessListIsUnaffected(t *testing.T) {
	e := testEngine(t, hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a")))
	if got := request(t, e, "api.example.com", "/").StatusCode; got != http.StatusOK {
		t.Errorf("status = %d, want 200", got)
	}
}

func bcryptHash(password string) (string, error) {
	// MinCost: these tests exercise routing, not the strength of the hash.
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	return string(h), err
}

// TestBasicAuthIsCachedAcrossRequests covers the reason the cache exists:
// bcrypt on every request made a protected host serve about 2 requests a
// second. The cache must make repeat requests cheap without letting a wrong
// password through.
func TestBasicAuthIsCachedAcrossRequests(t *testing.T) {
	hash, err := bcryptHash("hunter2hunter2")
	if err != nil {
		t.Fatal(err)
	}
	list := &domain.AccessList{
		ID: 1, Name: "staff", UpdatedAt: time.Now(),
		BasicAuth: []domain.BasicAuthUser{{Username: "ops", PasswordHash: hash}},
	}
	list.Normalize()
	e := engineWith(t, hostWithAccess(t, list))

	creds := func(user, pass string) func(*http.Request) {
		return func(r *http.Request) {
			r.Header.Set("Authorization", "Basic "+
				base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
		}
	}

	for i := range 5 {
		if got := request(t, e, "api.example.com", "/", creds("ops", "hunter2hunter2")).StatusCode; got != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, got)
		}
	}
	if n := e.authCache.size(); n != 1 {
		t.Errorf("cache holds %d entries after one credential, want 1", n)
	}

	// A wrong password must never be admitted by a warm cache.
	if got := request(t, e, "api.example.com", "/", creds("ops", "wrong")).StatusCode; got != http.StatusUnauthorized {
		t.Error("a wrong password was admitted while the cache was warm")
	}
	// And a refusal must not be cached, or a corrected password would be
	// locked out for the TTL.
	if n := e.authCache.size(); n != 1 {
		t.Errorf("cache holds %d entries, want the refusal not to be stored", n)
	}
}

// TestEditingAListInvalidatesCachedCredentials is why the key carries the
// list's UpdatedAt: removing a user or changing a password must take effect at
// once, not after the TTL.
func TestEditingAListInvalidatesCachedCredentials(t *testing.T) {
	hash, err := bcryptHash("hunter2hunter2")
	if err != nil {
		t.Fatal(err)
	}
	updated := time.Now()
	list := &domain.AccessList{
		ID: 1, Name: "staff", UpdatedAt: updated,
		BasicAuth: []domain.BasicAuthUser{{Username: "ops", PasswordHash: hash}},
	}
	list.Normalize()
	cfg := hostWithAccess(t, list)
	e := engineWith(t, cfg)

	creds := func(r *http.Request) {
		r.Header.Set("Authorization", "Basic "+
			base64.StdEncoding.EncodeToString([]byte("ops:hunter2hunter2")))
	}
	if got := request(t, e, "api.example.com", "/", creds).StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}

	// The operator removes the user. The stored credential is now wrong,
	// and the cached grant must not survive it.
	revoked := *list
	revoked.BasicAuth = []domain.BasicAuthUser{
		{Username: "someone-else", PasswordHash: hash},
	}
	revoked.UpdatedAt = updated.Add(time.Second)
	cfg.AccessLists = map[int64]*domain.AccessList{revoked.ID: &revoked}
	e.Reload(cfg)

	if got := request(t, e, "api.example.com", "/", creds).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("status = %d after the user was removed, want 401", got)
	}
}

// ------------------------------------------------------------ access log ---

// captureRecorder collects what the engine decided to log.
type captureRecorder struct {
	mu      sync.Mutex
	entries []domain.AccessLogEntry
}

func (c *captureRecorder) Record(e domain.AccessLogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
}

func (c *captureRecorder) all() []domain.AccessLogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]domain.AccessLogEntry(nil), c.entries...)
}

func engineLogging(t *testing.T, h domain.Host) (*Engine, *captureRecorder) {
	t.Helper()
	rec := &captureRecorder{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(Options{
		Logger:     logger,
		Collector:  metrics.New(nopMetricsRepo{}, logger),
		AccessLog:  rec,
		MaxRetries: 2,
	})
	t.Cleanup(e.Close)
	e.Reload(Config{Hosts: []domain.Host{h}})
	return e, rec
}

func TestAccessLogIsOffUnlessTheHostAsksForIt(t *testing.T) {
	h := hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a"))
	e, rec := engineLogging(t, h)

	for range 3 {
		request(t, e, "api.example.com", "/")
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("recorded %d entries with logging off, want 0", len(got))
	}
}

func TestAccessLogRecordsARequest(t *testing.T) {
	h := hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a"))
	h.AccessLog = domain.AccessLogSettings{Enabled: true}
	e, rec := engineLogging(t, h)

	request(t, e, "api.example.com", "/orders/7", func(r *http.Request) {
		r.Header.Set("User-Agent", "curl/8")
	})

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(got))
	}
	e0 := got[0]
	if e0.Method != http.MethodGet || e0.Path != "/orders/7" || e0.Status != http.StatusOK {
		t.Errorf("entry = %+v, want the request as issued", e0)
	}
	if e0.HostID != h.ID {
		t.Errorf("hostId = %d, want %d", e0.HostID, h.ID)
	}
	if e0.ClientIP != "203.0.113.9" {
		t.Errorf("clientIp = %q, want the peer address", e0.ClientIP)
	}
	if e0.UserAgent != "curl/8" {
		t.Errorf("userAgent = %q", e0.UserAgent)
	}
	if e0.Upstream == "" {
		t.Error("the serving upstream was not recorded")
	}
}

// TestAccessLogOmitsTheQueryStringByDefault is the privacy decision made
// explicit: query strings routinely carry tokens, and this log is searchable
// from a web console.
func TestAccessLogOmitsTheQueryStringByDefault(t *testing.T) {
	h := hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a"))
	h.AccessLog = domain.AccessLogSettings{Enabled: true}
	e, rec := engineLogging(t, h)

	request(t, e, "api.example.com", "/reset?token=super-secret-value")

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(got))
	}
	if strings.Contains(got[0].Path, "super-secret-value") {
		t.Errorf("path %q leaked the query string", got[0].Path)
	}
	if got[0].Path != "/reset" {
		t.Errorf("path = %q, want just the path", got[0].Path)
	}
}

func TestAccessLogIncludesTheQueryWhenAsked(t *testing.T) {
	h := hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a"))
	h.AccessLog = domain.AccessLogSettings{Enabled: true, IncludeQuery: true}
	e, rec := engineLogging(t, h)

	request(t, e, "api.example.com", "/search?q=widgets")

	got := rec.all()
	if len(got) != 1 || got[0].Path != "/search?q=widgets" {
		t.Errorf("path = %q, want the query included", got[0].Path)
	}
}

func TestAccessLogRecordsFailures(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadAddr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()

	h := hostFor("api.example.com", domain.RoundRobin)
	h.AccessLog = domain.AccessLogSettings{Enabled: true}
	h.HealthCheck = domain.HealthCheck{Enabled: false}
	h.Upstreams = []domain.Upstream{
		{ID: 1, HostID: 1, Scheme: "http", Address: deadAddr, Weight: 1, Enabled: true},
	}
	e, rec := engineLogging(t, h)

	if got := request(t, e, "api.example.com", "/").StatusCode; got != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", got)
	}

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(got))
	}
	if got[0].Status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", got[0].Status)
	}
	// The reason a request failed is the whole point of looking it up.
	if got[0].Error == "" {
		t.Error("the failure reason was not recorded")
	}
}

// --------------------------------------------------------------- guardian ---

func hostWithGuardian(t *testing.T, mode domain.GuardianMode, rules ...domain.GuardianRule) domain.Host {
	t.Helper()
	h := hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a"))
	h.Guardian = domain.Guardian{Mode: mode, Rules: rules, MaxURILength: 2048}
	return h
}

func TestGuardianBlocksAnAttack(t *testing.T) {
	e := testEngine(t, hostWithGuardian(t, domain.GuardianBlock, domain.RuleSensitiveFiles))

	if got := request(t, e, "api.example.com", "/.env").StatusCode; got != http.StatusForbidden {
		t.Errorf("status = %d, want 403", got)
	}
	// The refusal must not say which rule fired: that hands a prober the
	// shape of the filter.
	resp := request(t, e, "api.example.com", "/.git/config")
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(strings.ToLower(string(body)), "sensitive") ||
		strings.Contains(string(body), ".git") {
		t.Errorf("the refusal described the rule: %q", body)
	}
}

// TestGuardianDetectModeForwards is why detect exists: an operator must be
// able to watch what would be blocked before enforcing anything.
func TestGuardianDetectModeForwards(t *testing.T) {
	var reached atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	h := hostFor("api.example.com", domain.RoundRobin)
	h.Upstreams = []domain.Upstream{{
		ID: 1, HostID: 1, Scheme: "http",
		Address: strings.TrimPrefix(backend.URL, "http://"), Weight: 1, Enabled: true,
	}}
	h.Guardian = domain.Guardian{
		Mode:  domain.GuardianDetect,
		Rules: []domain.GuardianRule{domain.RuleSensitiveFiles},
	}
	e := testEngine(t, h)

	if got := request(t, e, "api.example.com", "/.env").StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d in detect mode, want the request forwarded", got)
	}
	if reached.Load() != 1 {
		t.Error("detect mode did not forward the request to the upstream")
	}
}

func TestGuardianOffInspectsNothing(t *testing.T) {
	e := testEngine(t, hostWithGuardian(t, domain.GuardianOff, domain.RuleSensitiveFiles))
	if got := request(t, e, "api.example.com", "/.env").StatusCode; got != http.StatusOK {
		t.Errorf("status = %d with inspection off, want 200", got)
	}
}

func TestGuardianLeavesOrdinaryTrafficAlone(t *testing.T) {
	e := testEngine(t, hostWithGuardian(t, domain.GuardianBlock, domain.GuardianRules()...))

	for _, path := range []string{
		"/", "/api/v1/orders?page=2", "/products/shoes",
		"/search?q=how+to+select+a+union+representative",
		"/.well-known/acme-challenge/tok",
	} {
		if got := request(t, e, "api.example.com", path).StatusCode; got != http.StatusOK {
			t.Errorf("%s was refused with %d", path, got)
		}
	}
}
