package proxy

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

var nextLimitHostID atomic.Int64

// limitRoute wires an engine to one backend with the given limits and returns
// the installed route, plus the backend's hit counter — the only measurement
// that says whether a refusal actually spared the backend.
func limitRoute(t *testing.T, e *Engine, limits domain.TrafficLimits) (*route, *atomic.Int64) {
	t.Helper()

	var hits atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	id := 9000 + nextLimitHostID.Add(1)
	name := "limit" + strconv.FormatInt(id, 10) + ".example.com"

	limits.Normalize()
	h := domain.Host{
		ID: id, Name: name, Enabled: true,
		Domains:       []string{name},
		Algorithm:     domain.RoundRobin,
		HealthCheck:   domain.HealthCheck{Enabled: false},
		TrafficLimits: limits,
		Upstreams: []domain.Upstream{{
			ID: id, HostID: id, Scheme: "http",
			Address: strings.TrimPrefix(backend.URL, "http://"),
			Weight:  1, Enabled: true,
		}},
	}
	e.Reload(Config{Hosts: []domain.Host{h}})

	rt := e.table.Load().lookup(name)
	if rt == nil {
		t.Fatal("route was not installed")
	}
	return rt, &hits
}

// get runs one request through the engine, from the same client each time.
func get(t *testing.T, e *Engine, rt *route, from string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://"+rt.host.Domains[0]+"/", nil)
	r.RemoteAddr = from + ":51000"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

func TestBlockModeRefusesPastTheBurstAndSparesTheBackend(t *testing.T) {
	e := testEngine(t)
	rt, hits := limitRoute(t, e, domain.TrafficLimits{
		Mode: domain.ModeBlock, RequestsPerSecond: 1, Burst: 3,
	})

	for i := range 3 {
		if got := get(t, e, rt, "203.0.113.1").Code; got != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i+1, got)
		}
	}

	rec := get(t, e, rt, "203.0.113.1")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the fourth request = %d, want 429", rec.Code)
	}
	// A refusal without Retry-After leaves a well-behaved client guessing,
	// and a badly behaved one retrying immediately.
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 429 should carry Retry-After")
	}

	// The point of refusing here rather than at the backend.
	if got := hits.Load(); got != 3 {
		t.Errorf("the backend saw %d requests, want 3", got)
	}
}

func TestDetectModeRefusesNothing(t *testing.T) {
	e := testEngine(t)
	rt, hits := limitRoute(t, e, domain.TrafficLimits{
		Mode: domain.ModeDetect, RequestsPerSecond: 1, Burst: 1,
	})

	for i := range 6 {
		if got := get(t, e, rt, "203.0.113.2").Code; got != http.StatusOK {
			t.Fatalf("request %d = %d, want 200: detect must never refuse", i+1, got)
		}
	}
	if got := hits.Load(); got != 6 {
		t.Errorf("the backend saw %d requests, want all 6", got)
	}
}

func TestOffModeIsNotConsulted(t *testing.T) {
	e := testEngine(t)
	rt, _ := limitRoute(t, e, domain.TrafficLimits{
		Mode: domain.ModeOff, RequestsPerSecond: 1, Burst: 1,
	})

	for range 20 {
		if got := get(t, e, rt, "203.0.113.3").Code; got != http.StatusOK {
			t.Fatalf("a host with limits off was limited anyway (%d)", got)
		}
	}
	// Nothing should have been recorded, which also proves the limiter's
	// table is not growing for hosts that do not use it.
	if got := e.limiter.Tracked(); got != 0 {
		t.Errorf("limiter tracked %d clients for a host with limits off", got)
	}
}

func TestExemptAddressesAreNeverLimited(t *testing.T) {
	e := testEngine(t)
	rt, _ := limitRoute(t, e, domain.TrafficLimits{
		Mode: domain.ModeBlock, RequestsPerSecond: 1, Burst: 1,
		// The monitoring probe. Without this, switching limits on takes
		// out your own uptime checks first: they are the most regular
		// traffic a host receives.
		Exempt: []string{"198.51.100.0/24", "203.0.113.50"},
	})

	for i := range 25 {
		if got := get(t, e, rt, "198.51.100.9").Code; got != http.StatusOK {
			t.Fatalf("exempt range request %d = %d, want 200", i+1, got)
		}
	}
	for i := range 25 {
		if got := get(t, e, rt, "203.0.113.50").Code; got != http.StatusOK {
			t.Fatalf("exempt address request %d = %d, want 200", i+1, got)
		}
	}
	// A non-exempt client still hits the same limit, so the exemption is
	// an exemption rather than the limiter being off.
	get(t, e, rt, "203.0.113.51")
	if got := get(t, e, rt, "203.0.113.51").Code; got != http.StatusTooManyRequests {
		t.Errorf("a non-exempt client = %d, want 429", got)
	}
}

func TestOneClientDoesNotSpendAnothersBudget(t *testing.T) {
	e := testEngine(t)
	rt, _ := limitRoute(t, e, domain.TrafficLimits{
		Mode: domain.ModeBlock, RequestsPerSecond: 1, Burst: 2,
	})

	get(t, e, rt, "203.0.113.10")
	get(t, e, rt, "203.0.113.10")
	if got := get(t, e, rt, "203.0.113.10").Code; got != http.StatusTooManyRequests {
		t.Fatalf("the noisy client should be limited, got %d", got)
	}
	if got := get(t, e, rt, "203.0.113.11").Code; got != http.StatusOK {
		t.Errorf("an unrelated visitor = %d, want 200", got)
	}
}

func TestAnOversizedBodyIsRefusedBeforeItIsRead(t *testing.T) {
	e := testEngine(t)
	rt, hits := limitRoute(t, e, domain.TrafficLimits{
		Mode: domain.ModeBlock, MaxBodyBytes: 16,
	})

	body := strings.Repeat("x", 4096)
	r := httptest.NewRequest(http.MethodPost, "http://"+rt.host.Domains[0]+"/upload",
		strings.NewReader(body))
	r.RemoteAddr = "203.0.113.20:51000"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	// Telling a client to retry an upload that is too big just wastes the
	// bandwidth a second time.
	if rec.Header().Get("Retry-After") != "" {
		t.Error("413 should not carry Retry-After")
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("the backend saw %d requests; an oversized body should never reach it", got)
	}
}

func TestABodyInsideTheLimitIsForwarded(t *testing.T) {
	e := testEngine(t)
	rt, hits := limitRoute(t, e, domain.TrafficLimits{
		Mode: domain.ModeBlock, MaxBodyBytes: 4096,
	})

	r := httptest.NewRequest(http.MethodPost, "http://"+rt.host.Domains[0]+"/upload",
		strings.NewReader(strings.Repeat("x", 512)))
	r.RemoteAddr = "203.0.113.21:51000"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("the backend saw %d requests, want 1", got)
	}
}

func TestLimitsAreCountedForTheConsole(t *testing.T) {
	e := testEngine(t)
	rt, _ := limitRoute(t, e, domain.TrafficLimits{
		Mode: domain.ModeDetect, RequestsPerSecond: 1, Burst: 1,
	})

	for range 5 {
		get(t, e, rt, "203.0.113.30")
	}

	var limited, blocked uint64
	for _, h := range e.opts.Collector.Snapshot().Hosts {
		if h.HostID == rt.host.ID {
			limited, blocked = h.LimitedRequests, h.BlockedRequests
		}
	}
	if limited == 0 {
		t.Error("detect mode recorded nothing; there would be no way to judge the setting")
	}
	// The gap between the two is the whole value of detect: what would have
	// been refused, against what was.
	if blocked != 0 {
		t.Errorf("detect mode reported %d blocked, want 0", blocked)
	}
}

func TestSwitchingLimitsOffForgetsSpentBudgets(t *testing.T) {
	e := testEngine(t)
	rt, _ := limitRoute(t, e, domain.TrafficLimits{
		Mode: domain.ModeBlock, RequestsPerSecond: 1, Burst: 1,
	})
	get(t, e, rt, "203.0.113.40")
	if e.limiter.Tracked() == 0 {
		t.Fatal("the client should be tracked while limits are on")
	}

	off := rt.host
	off.TrafficLimits.Mode = domain.ModeOff
	e.Reload(Config{Hosts: []domain.Host{off}})

	if got := e.limiter.Tracked(); got != 0 {
		t.Errorf("tracked %d clients after limits were switched off, want 0", got)
	}
}
