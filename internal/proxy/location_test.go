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

var nextLocHostID atomic.Int64

// echoBackend answers with a name and the path it was asked for, which is what
// makes it possible to assert both which backend served a request and what it
// saw after any prefix was stripped.
func echoBackend(t *testing.T, name string) (address string, hits *atomic.Int64) {
	t.Helper()
	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(name + " " + r.URL.Path))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &count
}

// locHost installs a host whose own upstreams serve "/" and whose locations
// serve their own prefixes.
func locHost(t *testing.T, e *Engine, root string, locs []domain.Location) *route {
	t.Helper()
	id := 5000 + nextLocHostID.Add(1)
	name := "loc" + strconv.FormatInt(id, 10) + ".example.com"

	h := domain.Host{
		ID: id, Name: name, Enabled: true,
		Domains:     []string{name},
		Algorithm:   domain.RoundRobin,
		HealthCheck: domain.HealthCheck{Enabled: false},
		Upstreams: []domain.Upstream{{
			ID: id, HostID: id, Scheme: "http", Address: root, Weight: 1, Enabled: true,
		}},
		Locations: locs,
	}
	h.Normalize()
	e.Reload(Config{Hosts: []domain.Host{h}})

	rt := e.table.Load().lookup(name)
	if rt == nil {
		t.Fatal("route was not installed")
	}
	return rt
}

func loc(path string, strip bool, address string) domain.Location {
	return domain.Location{
		Path: path, StripPrefix: strip,
		Upstreams: []domain.Upstream{{
			Scheme: "http", Address: address, Weight: 1, Enabled: true,
		}},
	}
}

// ask sends one request and returns what the backend replied.
func ask(t *testing.T, e *Engine, rt *route, path string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://"+rt.host.Domains[0]+path, nil)
	r.RemoteAddr = "203.0.113.1:53000"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

func TestAPathGoesToItsOwnBackend(t *testing.T) {
	e := testEngine(t)
	rootAddr, rootHits := echoBackend(t, "frontend")
	apiAddr, apiHits := echoBackend(t, "api")

	rt := locHost(t, e, rootAddr, []domain.Location{loc("/api", false, apiAddr)})

	if _, body := ask(t, e, rt, "/"); !strings.HasPrefix(body, "frontend") {
		t.Errorf("/ was served by %q, want the frontend", body)
	}
	if _, body := ask(t, e, rt, "/api/users"); !strings.HasPrefix(body, "api") {
		t.Errorf("/api/users was served by %q, want the api", body)
	}
	if rootHits.Load() != 1 || apiHits.Load() != 1 {
		t.Errorf("frontend saw %d and api saw %d; want one each",
			rootHits.Load(), apiHits.Load())
	}
}

func TestAPrefixDoesNotClaimALongerWord(t *testing.T) {
	e := testEngine(t)
	rootAddr, _ := echoBackend(t, "frontend")
	apiAddr, _ := echoBackend(t, "api")

	rt := locHost(t, e, rootAddr, []domain.Location{loc("/api", false, apiAddr)})

	// The classic prefix-routing bug: "/api" quietly swallowing "/apiary".
	// It only shows on paths nobody tested, and then it is one service
	// answering for another.
	for _, path := range []string{"/apiary", "/apifoo/bar", "/api-docs"} {
		_, body := ask(t, e, rt, path)
		if !strings.HasPrefix(body, "frontend") {
			t.Errorf("%s was served by %q, want the frontend", path, body)
		}
	}
	// The prefix itself, and anything under it, still belongs to the API.
	for _, path := range []string{"/api", "/api/", "/api/v1/users"} {
		_, body := ask(t, e, rt, path)
		if !strings.HasPrefix(body, "api") {
			t.Errorf("%s was served by %q, want the api", path, body)
		}
	}
}

func TestStripPrefixHidesTheMountPointFromTheBackend(t *testing.T) {
	e := testEngine(t)
	rootAddr, _ := echoBackend(t, "frontend")
	apiAddr, _ := echoBackend(t, "api")

	rt := locHost(t, e, rootAddr, []domain.Location{loc("/api", true, apiAddr)})

	cases := map[string]string{
		"/api/v1/users": "/v1/users",
		"/api/":         "/",
		// A backend mounted at its root expects "/", not "". The empty
		// string produces a request line no server should have to guess at.
		"/api": "/",
	}
	for asked, want := range cases {
		_, body := ask(t, e, rt, asked)
		if got := strings.TrimPrefix(body, "api "); got != want {
			t.Errorf("%s reached the backend as %q, want %q", asked, got, want)
		}
	}
}

func TestWithoutStripThePathArrivesWhole(t *testing.T) {
	e := testEngine(t)
	rootAddr, _ := echoBackend(t, "frontend")
	apiAddr, _ := echoBackend(t, "api")

	rt := locHost(t, e, rootAddr, []domain.Location{loc("/api", false, apiAddr)})

	_, body := ask(t, e, rt, "/api/v1/users")
	if got := strings.TrimPrefix(body, "api "); got != "/api/v1/users" {
		t.Errorf("backend saw %q, want the whole path", got)
	}
}

func TestTheLongestMatchWins(t *testing.T) {
	e := testEngine(t)
	rootAddr, _ := echoBackend(t, "frontend")
	apiAddr, _ := echoBackend(t, "api")
	v2Addr, _ := echoBackend(t, "v2")

	// Declared shortest first on purpose: order in the form must not decide
	// which one serves a request.
	rt := locHost(t, e, rootAddr, []domain.Location{
		loc("/api", false, apiAddr),
		loc("/api/v2", false, v2Addr),
	})

	if _, body := ask(t, e, rt, "/api/v1/users"); !strings.HasPrefix(body, "api") {
		t.Errorf("/api/v1/users went to %q", body)
	}
	if _, body := ask(t, e, rt, "/api/v2/users"); !strings.HasPrefix(body, "v2") {
		t.Errorf("/api/v2/users went to %q, want the more specific location", body)
	}
}

func TestAHostWithNoLocationsIsUnchanged(t *testing.T) {
	e := testEngine(t)
	rootAddr, hits := echoBackend(t, "frontend")

	rt := locHost(t, e, rootAddr, nil)
	if len(rt.locations) != 0 {
		t.Fatalf("a host with no locations has %d", len(rt.locations))
	}
	for _, path := range []string{"/", "/api", "/anything/at/all"} {
		if code, body := ask(t, e, rt, path); code != http.StatusOK ||
			!strings.HasPrefix(body, "frontend") {
			t.Errorf("%s = %d %q", path, code, body)
		}
	}
	if hits.Load() != 3 {
		t.Errorf("backend saw %d requests, want 3", hits.Load())
	}
}

func TestLocationBackendsAppearInTheSnapshot(t *testing.T) {
	e := testEngine(t)
	rootAddr, _ := echoBackend(t, "frontend")
	apiAddr, _ := echoBackend(t, "api")

	rt := locHost(t, e, rootAddr, []domain.Location{loc("/api", false, apiAddr)})
	ask(t, e, rt, "/")
	ask(t, e, rt, "/api/x")

	var found, labelled int
	for _, p := range e.MetricsPools() {
		if p.HostID != rt.host.ID {
			continue
		}
		found = len(p.Upstreams)
		for _, u := range p.Upstreams {
			if u.Location == "/api" {
				labelled++
			}
		}
		// The counts have to include the location's backends too, or the
		// console would report a host as fully up while one of its paths
		// has nothing behind it.
		if p.Total != 2 {
			t.Errorf("total backends = %d, want 2", p.Total)
		}
	}
	if found != 2 {
		t.Errorf("snapshot has %d backends for the host, want 2", found)
	}
	if labelled != 1 {
		t.Errorf("%d backends carry the location label, want 1", labelled)
	}
}

func TestReloadKeepsALocationsPoolAlive(t *testing.T) {
	e := testEngine(t)
	rootAddr, _ := echoBackend(t, "frontend")
	apiAddr, _ := echoBackend(t, "api")

	rt := locHost(t, e, rootAddr, []domain.Location{loc("/api", false, apiAddr)})

	// A reload always builds fresh pools; what has to survive is the state
	// inside them. Take the location's only backend down the way the health
	// checker would.
	rt.locations[0].pool.Backends()[0].SetHealthy(false)

	// An unrelated edit — the host's name — must not resurrect it.
	edited := rt.host
	edited.Name = edited.Name + "-renamed"
	e.Reload(Config{Hosts: []domain.Host{edited}})

	after := e.table.Load().lookup(rt.host.Domains[0])
	if after == nil || len(after.locations) != 1 {
		t.Fatal("the location did not survive the reload")
	}
	if after.locations[0].pool.Backends()[0].Healthy() {
		t.Error("a location's health state was reset by an unrelated reload")
	}
	// And the host's own pool is unaffected by the location's state.
	if !after.pool.Backends()[0].Healthy() {
		t.Error("taking a location down also took the host's own backend down")
	}
}
