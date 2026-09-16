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

var nextPageHostID atomic.Int64

// pageRoute installs one host with the given maintenance and error-page
// settings, and returns the route plus the backend's hit counter — the only
// measurement that says whether a request was actually stopped at the proxy.
func pageRoute(t *testing.T, e *Engine, m domain.Maintenance, ep domain.ErrorPages, backendUp bool) (*route, *atomic.Int64) {
	t.Helper()

	var hits atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("from the backend"))
	}))
	t.Cleanup(backend.Close)

	address := strings.TrimPrefix(backend.URL, "http://")
	if !backendUp {
		backend.Close()
	}

	id := 7000 + nextPageHostID.Add(1)
	name := "page" + strconv.FormatInt(id, 10) + ".example.com"

	m.Normalize()
	ep.Normalize()
	h := domain.Host{
		ID: id, Name: name, Enabled: true,
		Domains:     []string{name},
		Algorithm:   domain.RoundRobin,
		HealthCheck: domain.HealthCheck{Enabled: false},
		Maintenance: m,
		ErrorPages:  ep,
		Upstreams: []domain.Upstream{{
			ID: id, HostID: id, Scheme: "http", Address: address, Weight: 1, Enabled: true,
		}},
	}
	e.Reload(Config{Hosts: []domain.Host{h}})

	rt := e.table.Load().lookup(name)
	if rt == nil {
		t.Fatal("route was not installed")
	}
	return rt, &hits
}

func fetch(e *Engine, rt *route, from string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://"+rt.host.Domains[0]+"/", nil)
	r.RemoteAddr = from + ":52000"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

func TestMaintenanceAnswersInsteadOfProxying(t *testing.T) {
	e := testEngine(t)
	rt, hits := pageRoute(t, e, domain.Maintenance{
		Enabled: true, StatusCode: 503,
		Title: "Down for maintenance", Message: "Back at 14:00.",
		RetryAfterSeconds: 600,
	}, domain.ErrorPages{}, true)

	rec := fetch(e, rt, "203.0.113.1")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	// 503 is the status that means "come back", and Retry-After is how a
	// crawler is told when. Without it the page is just an error.
	if got := rec.Header().Get("Retry-After"); got != "600" {
		t.Errorf("Retry-After = %q, want 600", got)
	}
	// A cached maintenance page outlives the maintenance and takes the site
	// down after it is over.
	if store := rec.Header().Get("Cache-Control"); !strings.Contains(store, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", store)
	}
	body := rec.Body.String()
	for _, want := range []string{"Down for maintenance", "Back at 14:00."} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not contain %q", want)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("the backend was asked %d times during maintenance", got)
	}
}

func TestMaintenanceLetsTheOperatorThrough(t *testing.T) {
	e := testEngine(t)
	rt, hits := pageRoute(t, e, domain.Maintenance{
		Enabled: true, StatusCode: 503, Title: "Down for maintenance",
		// Whoever is doing the work has to be able to check that it worked.
		AllowFrom: []string{"198.51.100.0/24", "203.0.113.9"},
	}, domain.ErrorPages{}, true)

	for _, from := range []string{"198.51.100.5", "203.0.113.9"} {
		rec := fetch(e, rt, from)
		if rec.Code != http.StatusOK {
			t.Errorf("%s got %d, want 200: the bypass did not work", from, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "from the backend") {
			t.Errorf("%s was not served by the backend", from)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("the backend saw %d requests, want 2", got)
	}

	// Everyone else still gets the page, or the bypass is just an off switch.
	if rec := fetch(e, rt, "203.0.113.10"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("an ordinary visitor got %d, want 503", rec.Code)
	}
}

func TestMaintenanceOffChangesNothing(t *testing.T) {
	e := testEngine(t)
	rt, hits := pageRoute(t, e, domain.Maintenance{
		// Wording is present but the switch is off: the settings must be
		// inert until someone asks for them.
		Enabled: false, StatusCode: 503, Title: "Down for maintenance",
	}, domain.ErrorPages{}, true)

	if rec := fetch(e, rt, "203.0.113.2"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("the backend saw %d requests, want 1", got)
	}
}

func TestMaintenanceIsNotCountedAsAFailure(t *testing.T) {
	e := testEngine(t)
	rt, _ := pageRoute(t, e, domain.Maintenance{
		Enabled: true, StatusCode: 503, Title: "Down for maintenance",
	}, domain.ErrorPages{}, true)

	for range 5 {
		fetch(e, rt, "203.0.113.3")
	}

	// Planned maintenance that drives the error rate up sets off exactly the
	// alerting the maintenance window was booked to avoid.
	var errorRate float64
	for _, h := range e.opts.Collector.Snapshot().Hosts {
		if h.HostID == rt.host.ID {
			errorRate = h.Traffic.ErrorRate
		}
	}
	if errorRate != 0 {
		t.Errorf("error rate = %v during maintenance, want 0", errorRate)
	}
}

func TestACustomErrorPageReplacesTheBuiltInOne(t *testing.T) {
	e := testEngine(t)
	// Backend closed: every request will fail to connect.
	rt, _ := pageRoute(t, e, domain.Maintenance{}, domain.ErrorPages{
		Enabled: true,
		Title:   "We will be right back",
		Message: "Our team has been notified.",
	}, false)

	rec := fetch(e, rt, "203.0.113.4")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "We will be right back") {
		t.Error("the host's own wording was not used")
	}
	// The default text names the upstream, which is the proxy's business
	// and not the visitor's.
	if strings.Contains(body, "upstream") {
		t.Error("the built-in wording leaked into a custom page")
	}
}

func TestErrorPagesOffFallsBackToTheBuiltInWording(t *testing.T) {
	e := testEngine(t)
	rt, _ := pageRoute(t, e, domain.Maintenance{}, domain.ErrorPages{
		Enabled: false, Title: "Unused", Message: "Unused",
	}, false)

	rec := fetch(e, rt, "203.0.113.5")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Unused") {
		t.Error("wording was used from a page that is switched off")
	}
}

func TestPagesEscapeOperatorText(t *testing.T) {
	e := testEngine(t)
	// The title and message are operator input rendered into a page served
	// from the operator's own domain, where a stored script would run with
	// their cookies in scope.
	rt, _ := pageRoute(t, e, domain.Maintenance{
		Enabled: true, StatusCode: 503,
		Title:   `Down <script>alert(1)</script>`,
		Message: `Ask "ops" & wait`,
	}, domain.ErrorPages{}, true)

	body := fetch(e, rt, "203.0.113.6").Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("the title was rendered as markup")
	}
	if !strings.Contains(body, "&amp;") {
		t.Error("the ampersand was not escaped")
	}
	if !strings.Contains(body, "&#34;ops&#34;") && !strings.Contains(body, "&quot;ops&quot;") {
		t.Error("the quotes were not escaped")
	}
}

func TestPagesKeepLineBreaks(t *testing.T) {
	e := testEngine(t)
	rt, _ := pageRoute(t, e, domain.Maintenance{
		Enabled: true, StatusCode: 503, Title: "Maintenance",
		Message: "First line.\nSecond line.",
	}, domain.ErrorPages{}, true)

	body := fetch(e, rt, "203.0.113.7").Body.String()
	// Operators write more than one sentence and expect the break to
	// survive; a page that runs it together looks broken.
	if !strings.Contains(body, "First line.<br>Second line.") {
		t.Error("a line break in the message was not kept")
	}
}

func TestPageIsSelfContained(t *testing.T) {
	// These pages are served exactly when nothing else works, so anything
	// they fetch from elsewhere is something that can fail with them.
	body := renderPage("Down", "Back soon", 503)
	for _, forbidden := range []string{"http://", "https://", "<img", "<script", "<link"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the page contains %q, so it depends on something external", forbidden)
		}
	}
	if !strings.Contains(body, "prefers-color-scheme") {
		t.Error("the page ignores the visitor's dark mode")
	}
}
