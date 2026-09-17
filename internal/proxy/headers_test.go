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

var nextHeaderHostID atomic.Int64

// headerHost installs a host that echoes back the request headers it received,
// so a test can assert on both directions from one response.
func headerHost(t *testing.T, e *Engine, hostHeaders domain.Headers, locs []domain.Location) *route {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range r.Header {
			w.Header().Set("Echo-"+name, strings.Join(values, ","))
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Server", "backend/1.0")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(backend.Close)

	id := 6000 + nextHeaderHostID.Add(1)
	name := "hdr" + strconv.FormatInt(id, 10) + ".example.com"

	h := domain.Host{
		ID: id, Name: name, Enabled: true,
		Domains:     []string{name},
		Algorithm:   domain.RoundRobin,
		HealthCheck: domain.HealthCheck{Enabled: false},
		Headers:     hostHeaders,
		Locations:   locs,
		Upstreams: []domain.Upstream{{
			ID: id, HostID: id, Scheme: "http",
			Address: strings.TrimPrefix(backend.URL, "http://"), Weight: 1, Enabled: true,
		}},
	}
	h.Normalize()
	e.Reload(Config{Hosts: []domain.Host{h}})

	rt := e.table.Load().lookup(name)
	if rt == nil {
		t.Fatal("route was not installed")
	}
	return rt
}

func fetchPath(t *testing.T, e *Engine, rt *route, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://"+rt.host.Domains[0]+path, nil)
	r.RemoteAddr = "203.0.113.1:55000"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

func TestARequestHeaderReachesTheBackend(t *testing.T) {
	e := testEngine(t)
	rt := headerHost(t, e, domain.Headers{
		Request: []domain.HeaderRule{
			{Name: "X-Tenant-Id", Value: "acme"},
		},
	}, nil)

	rec := fetchPath(t, e, rt, "/")
	if got := rec.Header().Get("Echo-X-Tenant-Id"); got != "acme" {
		t.Errorf("the backend saw X-Tenant-Id = %q, want acme", got)
	}
}

func TestAResponseHeaderReachesTheVisitor(t *testing.T) {
	e := testEngine(t)
	rt := headerHost(t, e, domain.Headers{
		Response: []domain.HeaderRule{
			{Name: "X-Frame-Options", Value: "DENY"},
			// Removing what the backend volunteers about itself is the
			// other half of why anyone wants this.
			{Name: "Server", Remove: true},
		},
	}, nil)

	rec := fetchPath(t, e, rt, "/")
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("Server"); got != "" {
		t.Errorf("Server = %q, want it removed", got)
	}
}

func TestARuleReplacesRatherThanAppends(t *testing.T) {
	e := testEngine(t)
	// The backend already sends a Server header. A rule that appended would
	// produce two values and no way to predict which one is read.
	rt := headerHost(t, e, domain.Headers{
		Response: []domain.HeaderRule{{Name: "Server", Value: "ponzproxy"}},
	}, nil)

	rec := fetchPath(t, e, rt, "/")
	if got := rec.Header().Values("Server"); len(got) != 1 || got[0] != "ponzproxy" {
		t.Errorf("Server = %v, want exactly [ponzproxy]", got)
	}
}

func TestALocationsHeadersAreAppliedAfterTheHosts(t *testing.T) {
	e := testEngine(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Echo-X-Scope", r.Header.Get("X-Scope"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	rt := headerHost(t, e, domain.Headers{
		Request:  []domain.HeaderRule{{Name: "X-Scope", Value: "site"}},
		Response: []domain.HeaderRule{{Name: "X-Layer", Value: "host"}},
	}, []domain.Location{{
		Path: "/api",
		Upstreams: []domain.Upstream{{
			Scheme: "http", Address: strings.TrimPrefix(backend.URL, "http://"),
			Weight: 1, Enabled: true,
		}},
		Headers: domain.Headers{
			// A location overriding one of the host's rules for its own
			// path is the point of applying them in this order.
			Request:  []domain.HeaderRule{{Name: "X-Scope", Value: "api"}},
			Response: []domain.HeaderRule{{Name: "X-Layer", Value: "location"}},
		},
	}})

	root := fetchPath(t, e, rt, "/")
	if got := root.Header().Get("Echo-X-Scope"); got != "site" {
		t.Errorf("/ sent X-Scope = %q, want the host's own value", got)
	}
	if got := root.Header().Get("X-Layer"); got != "host" {
		t.Errorf("/ returned X-Layer = %q, want host", got)
	}

	api := fetchPath(t, e, rt, "/api/x")
	if got := api.Header().Get("Echo-X-Scope"); got != "api" {
		t.Errorf("/api sent X-Scope = %q, want the location's override", got)
	}
	if got := api.Header().Get("X-Layer"); got != "location" {
		t.Errorf("/api returned X-Layer = %q, want location", got)
	}
}

func TestNoRulesChangesNothing(t *testing.T) {
	e := testEngine(t)
	rt := headerHost(t, e, domain.Headers{}, nil)

	rec := fetchPath(t, e, rt, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The backend's own headers arrive untouched.
	if got := rec.Header().Get("Server"); got != "backend/1.0" {
		t.Errorf("Server = %q, want the backend's own", got)
	}
}

func TestProtectedHeadersAreRefusedBeforeTheyReachTheProxy(t *testing.T) {
	// These describe the connection or the framing rather than the content.
	// Setting one does not change what the proxy does; it only makes the
	// headers disagree with reality, and Content-Length is the clearest
	// case — writing one does not change the body.
	for _, name := range []string{
		"Content-Length", "Connection", "Transfer-Encoding", "Upgrade", "Host",
	} {
		h := domain.Headers{Response: []domain.HeaderRule{{Name: name, Value: "x"}}}
		h.Normalize()

		v := &domain.ValidationError{}
		h.Validate(v, "headers")
		if v.Err() == nil {
			t.Errorf("%s was accepted as a header rule", name)
		}
	}
}

func TestAHeaderValueCannotInjectAnotherHeader(t *testing.T) {
	// A newline in a value ends the header and starts one the operator did
	// not write.
	h := domain.Headers{Response: []domain.HeaderRule{
		{Name: "X-Thing", Value: "ok\r\nX-Injected: yes"},
	}}
	h.Normalize()

	v := &domain.ValidationError{}
	h.Validate(v, "headers")
	if v.Err() == nil {
		t.Error("a value containing CRLF was accepted")
	}
}
