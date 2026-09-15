package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/metrics"
)

func testEngine(t *testing.T, hosts ...domain.Host) *Engine {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(Options{
		Logger:     logger,
		Collector:  metrics.New(nopMetricsRepo{}, logger),
		MaxRetries: 2,
	})
	t.Cleanup(e.Close)
	e.Reload(Config{Hosts: hosts})
	return e
}

// backendServer returns a server that echoes what the proxy sent it.
func backendServer(t *testing.T, id string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", id)
		w.Header().Set("X-Got-Host", r.Host)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "hello from "+id)
	}))
	t.Cleanup(s.Close)
	return s
}

func hostFor(name string, alg domain.Algorithm, servers ...*httptest.Server) domain.Host {
	h := domain.Host{
		ID: 1, Name: name, Enabled: true,
		Domains:   []string{name},
		Algorithm: alg,
		// Probing is left off: these tests drive health explicitly.
		HealthCheck:      domain.HealthCheck{Enabled: false},
		WebSocketSupport: true,
	}
	for i, s := range servers {
		h.Upstreams = append(h.Upstreams, domain.Upstream{
			ID: int64(i + 1), HostID: 1, Scheme: "http",
			Address: strings.TrimPrefix(s.URL, "http://"),
			Weight:  1, Enabled: true,
		})
	}
	return h
}

func do(t *testing.T, e *Engine, method, host, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	req.Host = host
	req.RemoteAddr = "203.0.113.9:5555"
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w.Result()
}

// nopMetricsRepo satisfies the metrics repository without persisting: these
// tests exercise the data plane, not storage.
type nopMetricsRepo struct{}

func (nopMetricsRepo) WriteSamples(context.Context, []domain.Sample) error { return nil }

func (nopMetricsRepo) Query(context.Context, domain.MetricsQuery) ([]domain.Sample, error) {
	return nil, nil
}

func (nopMetricsRepo) Prune(context.Context, time.Time) (int64, error) { return 0, nil }

func TestProxyForwardsAndSpreadsLoad(t *testing.T) {
	a, b := backendServer(t, "a"), backendServer(t, "b")
	e := testEngine(t, hostFor("api.example.com", domain.RoundRobin, a, b))

	seen := map[string]int{}
	for range 6 {
		resp := do(t, e, http.MethodGet, "api.example.com", "/")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.HasPrefix(string(body), "hello from ") {
			t.Fatalf("unexpected body %q", body)
		}
		seen[resp.Header.Get("X-Backend")]++
	}
	if seen["a"] != 3 || seen["b"] != 3 {
		t.Errorf("distribution = %v, want 3/3", seen)
	}
}

func TestUnknownDomainGets404(t *testing.T) {
	e := testEngine(t, hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a")))

	resp := do(t, e, http.MethodGet, "nope.example.com", "/")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestWildcardAndExactPrecedence(t *testing.T) {
	exact, wild := backendServer(t, "exact"), backendServer(t, "wild")

	exactHost := hostFor("api.example.com", domain.RoundRobin, exact)
	wildHost := hostFor("*.example.com", domain.RoundRobin, wild)
	wildHost.ID, wildHost.Upstreams[0].HostID = 2, 2

	e := testEngine(t, exactHost, wildHost)

	if got := do(t, e, http.MethodGet, "api.example.com", "/").Header.Get("X-Backend"); got != "exact" {
		t.Errorf("api.example.com went to %q, want the exact match", got)
	}
	if got := do(t, e, http.MethodGet, "other.example.com", "/").Header.Get("X-Backend"); got != "wild" {
		t.Errorf("other.example.com went to %q, want the wildcard", got)
	}
	// A wildcard covers one label only.
	if got := do(t, e, http.MethodGet, "a.b.example.com", "/").StatusCode; got != http.StatusNotFound {
		t.Errorf("a.b.example.com status = %d, want 404 (wildcards cover one label)", got)
	}
	// The apex is not covered by "*.example.com" either.
	if got := do(t, e, http.MethodGet, "example.com", "/").StatusCode; got != http.StatusNotFound {
		t.Errorf("example.com status = %d, want 404", got)
	}
}

func TestHostHeaderWithPortStillRoutes(t *testing.T) {
	e := testEngine(t, hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a")))

	if got := do(t, e, http.MethodGet, "api.example.com:8443", "/").StatusCode; got != http.StatusOK {
		t.Errorf("status = %d, want 200 — the port must be ignored when routing", got)
	}
}

func TestDeadBackendIsRetriedOnAnother(t *testing.T) {
	good := backendServer(t, "good")
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadAddr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close() // nothing listens here any more

	h := hostFor("api.example.com", domain.RoundRobin, good)
	h.Upstreams = append([]domain.Upstream{{
		ID: 2, HostID: 1, Scheme: "http", Address: deadAddr, Weight: 1, Enabled: true,
	}}, h.Upstreams...)

	e := testEngine(t, h)

	// Round robin hits the dead backend first; the retry must rescue it.
	for i := range 4 {
		resp := do(t, e, http.MethodGet, "api.example.com", "/")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 via the retry", i, resp.StatusCode)
		}
		if got := resp.Header.Get("X-Backend"); got != "good" {
			t.Fatalf("request %d served by %q, want the live backend", i, got)
		}
	}
}

func TestAllBackendsDownGives502(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()

	h := hostFor("api.example.com", domain.RoundRobin)
	h.Upstreams = []domain.Upstream{
		{ID: 1, HostID: 1, Scheme: "http", Address: addr, Weight: 1, Enabled: true},
	}
	e := testEngine(t, h)

	resp := do(t, e, http.MethodGet, "api.example.com", "/")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), addr) {
		t.Error("the error page leaked the upstream address to the client")
	}
}

func TestNoHealthyUpstreamGives503(t *testing.T) {
	e := testEngine(t, hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a")))

	// Take the only backend out of rotation the way the health checker would.
	for _, r := range e.table.Load().routes {
		for _, b := range r.pool.Backends() {
			b.SetHealthy(false)
		}
	}

	if got := do(t, e, http.MethodGet, "api.example.com", "/").StatusCode; got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", got)
	}
}

func TestRequestWithBodyIsNotReplayed(t *testing.T) {
	var deliveries atomic.Int64
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != "payload" {
			t.Errorf("backend received %q, want the full body", body)
		}
		deliveries.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadAddr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()

	h := hostFor("api.example.com", domain.RoundRobin)
	h.Upstreams = []domain.Upstream{
		{ID: 1, HostID: 1, Scheme: "http", Address: deadAddr, Weight: 1, Enabled: true},
		{ID: 2, HostID: 1, Scheme: "http", Address: strings.TrimPrefix(good.URL, "http://"), Weight: 1, Enabled: true},
	}
	e := testEngine(t, h)

	req := httptest.NewRequest(http.MethodPost, "http://api.example.com/", strings.NewReader("payload"))
	req.Host = "api.example.com"
	req.RemoteAddr = "203.0.113.9:5555"
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	// The dead backend is first, so this must fail rather than replay a
	// body that can no longer be rewound.
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 — a request with a body must not be replayed", w.Code)
	}
	if n := deliveries.Load(); n != 0 {
		t.Errorf("the body was delivered %d times, want 0", n)
	}
}

func TestPreserveHostControlsUpstreamHostHeader(t *testing.T) {
	backend := backendServer(t, "a")

	h := hostFor("api.example.com", domain.RoundRobin, backend)
	e := testEngine(t, h)
	if got := do(t, e, http.MethodGet, "api.example.com", "/").Header.Get("X-Got-Host"); got == "api.example.com" {
		t.Error("host header was preserved without PreserveHost being set")
	}

	h.PreserveHost = true
	e.Reload(Config{Hosts: []domain.Host{h}})
	if got := do(t, e, http.MethodGet, "api.example.com", "/").Header.Get("X-Got-Host"); got != "api.example.com" {
		t.Errorf("upstream saw host %q, want api.example.com", got)
	}
}

func TestForwardedHeadersAreSetAndNotSpoofable(t *testing.T) {
	var gotXFF, gotProto string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
		gotProto = r.Header.Get("X-Forwarded-Proto")
	}))
	defer backend.Close()

	h := hostFor("api.example.com", domain.RoundRobin)
	h.Upstreams = []domain.Upstream{{
		ID: 1, HostID: 1, Scheme: "http",
		Address: strings.TrimPrefix(backend.URL, "http://"), Weight: 1, Enabled: true,
	}}
	e := testEngine(t, h)

	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/", nil)
	req.Host = "api.example.com"
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4") // a client trying to lie
	e.ServeHTTP(httptest.NewRecorder(), req)

	if !strings.HasSuffix(gotXFF, "203.0.113.9") {
		t.Errorf("X-Forwarded-For = %q, want it to end with the real peer address", gotXFF)
	}
	if gotProto != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want http", gotProto)
	}
}

func TestUntrustedForwardedHeaderDoesNotSteerIPHash(t *testing.T) {
	e := testEngine(t, hostFor("api.example.com", domain.IPHash,
		backendServer(t, "a"), backendServer(t, "b")))

	// Same peer, different spoofed XFF values: routing must not move.
	first := ""
	for i := range 20 {
		req := httptest.NewRequest(http.MethodGet, "http://api.example.com/", nil)
		req.Host = "api.example.com"
		req.RemoteAddr = "203.0.113.9:5555"
		req.Header.Set("X-Forwarded-For", "10.0.0."+string(rune('0'+i%10)))
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)

		got := w.Header().Get("X-Backend")
		if first == "" {
			first = got
		} else if got != first {
			t.Fatalf("a spoofed X-Forwarded-For moved the client from %q to %q", first, got)
		}
	}
}

func TestForceHTTPSRedirects(t *testing.T) {
	h := hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a"))
	certID := int64(1)
	h.CertificateID, h.ForceHTTPS = &certID, true
	e := testEngine(t, h)

	resp := do(t, e, http.MethodPost, "api.example.com", "/submit?x=1")
	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308 so the method and body survive", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "https://api.example.com/submit?x=1" {
		t.Errorf("Location = %q", got)
	}
}

func TestWebSocketUpgradeIsRefusedWhenDisabled(t *testing.T) {
	h := hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a"))
	h.WebSocketSupport = false
	e := testEngine(t, h)

	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/ws", nil)
	req.Host = "api.example.com"
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "keep-alive, Upgrade")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestReloadKeepsServingAndCarriesHealthOver(t *testing.T) {
	a := backendServer(t, "a")
	h := hostFor("api.example.com", domain.RoundRobin, a)
	e := testEngine(t, h)

	e.table.Load().routes[0].pool.Backends()[0].SetHealthy(false)

	// An unrelated edit must not resurrect a backend the checker took down.
	h.PreserveHost = true
	e.Reload(Config{Hosts: []domain.Host{h}})

	if e.table.Load().routes[0].pool.Backends()[0].Healthy() {
		t.Error("health state was reset by a configuration reload")
	}
	if got := do(t, e, http.MethodGet, "api.example.com", "/").StatusCode; got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 while the only backend is down", got)
	}
}

func TestDisabledHostStopsRouting(t *testing.T) {
	h := hostFor("api.example.com", domain.RoundRobin, backendServer(t, "a"))
	e := testEngine(t, h)
	if got := do(t, e, http.MethodGet, "api.example.com", "/").StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want 200 while enabled", got)
	}

	h.Enabled = false
	e.Reload(Config{Hosts: []domain.Host{h}})
	if got := do(t, e, http.MethodGet, "api.example.com", "/").StatusCode; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404 once disabled", got)
	}
	// It must still appear on the dashboard.
	if pools := e.MetricsPools(); len(pools) != 1 || pools[0].Enabled {
		t.Errorf("MetricsPools = %+v, want the host listed as disabled", pools)
	}
}

func TestSlowBackendStillStreams(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := range 3 {
			io.WriteString(w, "data: "+string(rune('0'+i))+"\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer backend.Close()

	h := hostFor("api.example.com", domain.RoundRobin)
	h.Upstreams = []domain.Upstream{{
		ID: 1, HostID: 1, Scheme: "http",
		Address: strings.TrimPrefix(backend.URL, "http://"), Weight: 1, Enabled: true,
	}}
	e := testEngine(t, h)

	resp := do(t, e, http.MethodGet, "api.example.com", "/events")
	body, _ := io.ReadAll(resp.Body)
	if n := strings.Count(string(body), "data: "); n != 3 {
		t.Errorf("got %d streamed events, want 3", n)
	}
}

// TestDeadBackendIsEjectedWithoutActiveProbing is the case passive health
// exists for: with health checks switched off, nothing else would ever take a
// dead backend out, and every request would keep paying a connection failure.
func TestDeadBackendIsEjectedWithoutActiveProbing(t *testing.T) {
	var reached atomic.Int64
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadAddr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close() // nothing listens here any more

	h := hostFor("api.example.com", domain.RoundRobin)
	h.HealthCheck = domain.HealthCheck{Enabled: false}
	h.PassiveHealth = domain.PassiveHealth{
		Enabled: true, MaxFails: 2, EjectFor: time.Minute,
	}
	h.Upstreams = []domain.Upstream{
		{ID: 1, HostID: 1, Scheme: "http", Address: deadAddr, Weight: 1, Enabled: true},
		{ID: 2, HostID: 1, Scheme: "http", Address: strings.TrimPrefix(good.URL, "http://"), Weight: 1, Enabled: true},
	}
	e := testEngine(t, h)

	backends := e.table.Load().routes[0].pool.Backends()
	deadBackend := backends[0]

	for i := range 12 {
		resp := do(t, e, http.MethodGet, "api.example.com", "/")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, resp.StatusCode)
		}
	}

	ejected, remaining := deadBackend.Ejected(time.Now())
	if !ejected {
		t.Fatal("the dead backend was never ejected")
	}
	if remaining <= 0 {
		t.Errorf("remaining ejection = %v, want a positive window", remaining)
	}
	// Health is the active probe's verdict, and no probe ran.
	if !deadBackend.Healthy() {
		t.Error("ejection was recorded as an active-probe failure")
	}
	// Every request still succeeded, and the live backend served them all.
	if got := reached.Load(); got != 12 {
		t.Errorf("the live backend served %d of 12 requests", got)
	}

	// Once ejected it must not be picked at all, so no further request pays
	// the cost of dialling it.
	for range 20 {
		b, err := e.table.Load().routes[0].pool.Pick("203.0.113.9", nil)
		if err != nil {
			t.Fatalf("pick: %v", err)
		}
		if b == deadBackend {
			t.Fatal("an ejected backend was selected again")
		}
	}
}

// TestEjectionRecoversWhenTheBackendComesBack checks the other half: once the
// window lapses and the backend answers, it returns to rotation.
func TestEjectionRecoversWhenTheBackendComesBack(t *testing.T) {
	backend := backendServer(t, "a")

	h := hostFor("api.example.com", domain.RoundRobin, backend)
	h.HealthCheck = domain.HealthCheck{Enabled: false}
	h.PassiveHealth = domain.PassiveHealth{
		Enabled: true, MaxFails: 1, EjectFor: 50 * time.Millisecond,
	}
	e := testEngine(t, h)
	b := e.table.Load().routes[0].pool.Backends()[0]

	// Eject it directly: the point here is the recovery path, not the
	// failure path, which the test above already covers.
	b.RecordProxyResult(false, h.PassiveHealth, time.Now())
	if got := do(t, e, http.MethodGet, "api.example.com", "/").StatusCode; got != http.StatusServiceUnavailable {
		t.Fatalf("status while ejected = %d, want 503", got)
	}

	time.Sleep(80 * time.Millisecond)
	if got := do(t, e, http.MethodGet, "api.example.com", "/").StatusCode; got != http.StatusOK {
		t.Errorf("status after the window lapsed = %d, want 200", got)
	}
}

// TestUpstreamErrorsDoNotEjectOnStatusCodes guards the rule that only
// connection failures count. A backend answering 500 is alive, and may be
// answering 500 because the request deserved it.
func TestUpstreamErrorsDoNotEjectOnStatusCodes(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer backend.Close()

	h := hostFor("api.example.com", domain.RoundRobin)
	h.HealthCheck = domain.HealthCheck{Enabled: false}
	h.PassiveHealth = domain.PassiveHealth{Enabled: true, MaxFails: 2, EjectFor: time.Minute}
	h.Upstreams = []domain.Upstream{{
		ID: 1, HostID: 1, Scheme: "http",
		Address: strings.TrimPrefix(backend.URL, "http://"), Weight: 1, Enabled: true,
	}}
	e := testEngine(t, h)

	for range 10 {
		if got := do(t, e, http.MethodGet, "api.example.com", "/").StatusCode; got != http.StatusInternalServerError {
			t.Fatalf("status = %d, want the upstream's 500 passed through", got)
		}
	}

	if ejected, _ := e.table.Load().routes[0].pool.Backends()[0].Ejected(time.Now()); ejected {
		t.Error("a backend was ejected for returning 500s, which it is entitled to do")
	}
}
