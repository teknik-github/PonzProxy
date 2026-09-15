package health

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/balancer"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fastCheck probes aggressively so tests finish in milliseconds.
func fastCheck() domain.HealthCheck {
	return domain.HealthCheck{
		Enabled:            true,
		Path:               "/healthz",
		Interval:           10 * time.Millisecond,
		Timeout:            200 * time.Millisecond,
		HealthyThreshold:   2,
		UnhealthyThreshold: 2,
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func poolFor(t *testing.T, servers ...*httptest.Server) *balancer.Pool {
	t.Helper()
	ups := make([]domain.Upstream, len(servers))
	for i, s := range servers {
		ups[i] = domain.Upstream{
			ID:      int64(i + 1),
			Scheme:  "http",
			Address: strings.TrimPrefix(s.URL, "http://"),
			Weight:  1,
			Enabled: true,
		}
	}
	return balancer.NewPool(1, domain.RoundRobin, ups, nil)
}

func TestCheckerMarksBackendDownAndBackUp(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("probed %q, want /healthz", r.URL.Path)
		}
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pool := poolFor(t, srv)
	backend := pool.Backends()[0]

	var transitions atomic.Int64
	c := New(discardLogger(), func(int64, *balancer.Backend, bool) { transitions.Add(1) })
	defer c.Close()

	c.Sync([]Target{{HostID: 1, Name: "api", Pool: pool, Check: fastCheck()}})

	// Stays up while the backend is serving.
	time.Sleep(50 * time.Millisecond)
	if !backend.Healthy() {
		t.Fatal("backend was taken down while it was serving 200s")
	}

	healthy.Store(false)
	waitFor(t, "backend to be marked down", func() bool { return !backend.Healthy() })
	if got := backend.LastError(); !strings.Contains(got, "503") {
		t.Errorf("last error = %q, want it to mention the 503", got)
	}

	healthy.Store(true)
	waitFor(t, "backend to recover", func() bool { return backend.Healthy() })
	if got := backend.LastError(); got != "" {
		t.Errorf("last error = %q, want it cleared after recovery", got)
	}
	if n := transitions.Load(); n != 2 {
		t.Errorf("observed %d transitions, want exactly 2 (down then up)", n)
	}
}

func TestUnhealthyThresholdAbsorbsASingleBlip(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.CompareAndSwap(true, false) { // fail exactly once
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pool := poolFor(t, srv)
	backend := pool.Backends()[0]

	check := fastCheck()
	check.UnhealthyThreshold = 3

	c := New(discardLogger(), nil)
	defer c.Close()
	c.Sync([]Target{{HostID: 1, Name: "api", Pool: pool, Check: check}})

	time.Sleep(30 * time.Millisecond)
	fail.Store(true)
	time.Sleep(100 * time.Millisecond)

	if !backend.Healthy() {
		t.Error("one failed probe took the backend down despite a threshold of 3")
	}
}

func TestExpectStatusIsHonoured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pool := poolFor(t, srv)
	check := fastCheck()
	check.ExpectStatus = http.StatusNoContent // the server returns 200

	c := New(discardLogger(), nil)
	defer c.Close()
	c.Sync([]Target{{HostID: 1, Name: "api", Pool: pool, Check: check}})

	waitFor(t, "backend to fail the exact-status check", func() bool {
		return !pool.Backends()[0].Healthy()
	})
}

func TestUnreachableBackendIsMarkedDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // nothing is listening on addr any more

	pool := balancer.NewPool(1, domain.RoundRobin, []domain.Upstream{
		{ID: 1, Scheme: "http", Address: addr, Weight: 1, Enabled: true},
	}, nil)

	c := New(discardLogger(), nil)
	defer c.Close()
	c.Sync([]Target{{HostID: 1, Name: "api", Pool: pool, Check: fastCheck()}})

	waitFor(t, "unreachable backend to be marked down", func() bool {
		return !pool.Backends()[0].Healthy()
	})
}

func TestSyncStartsStopsAndLeavesUnchangedTargetsAlone(t *testing.T) {
	var probes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pool := poolFor(t, srv)
	target := Target{HostID: 1, Name: "api", Pool: pool, Check: fastCheck()}

	c := New(discardLogger(), nil)
	defer c.Close()

	c.Sync([]Target{target})
	waitFor(t, "the first probe", func() bool { return probes.Load() > 0 })

	// Re-syncing an identical target must not restart the probe loop.
	c.Sync([]Target{target})
	c.Sync([]Target{target})
	before := probes.Load()
	time.Sleep(50 * time.Millisecond)
	if after := probes.Load(); after <= before {
		t.Error("probing stopped after an identical re-sync")
	}

	// Removing the target must stop probing entirely.
	c.Sync(nil)
	settled := probes.Load()
	time.Sleep(60 * time.Millisecond)
	if probes.Load() != settled {
		t.Errorf("probes continued after the host was removed: %d -> %d", settled, probes.Load())
	}
}

func TestDisabledHealthCheckDoesNotProbe(t *testing.T) {
	var probes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
	}))
	defer srv.Close()

	check := fastCheck()
	check.Enabled = false

	c := New(discardLogger(), nil)
	defer c.Close()
	c.Sync([]Target{{HostID: 1, Name: "api", Pool: poolFor(t, srv), Check: check}})

	time.Sleep(60 * time.Millisecond)
	if n := probes.Load(); n != 0 {
		t.Errorf("made %d probes with health checking disabled, want 0", n)
	}
}

// TestCloseIsSafeUnderConcurrentSync guards the reload path: Sync and Close
// race in production whenever the process shuts down during a config change.
func TestCloseIsSafeUnderConcurrentSync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(discardLogger(), nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for range 5 {
				c.Sync([]Target{{
					HostID: int64(i), Name: "api",
					Pool: poolFor(t, srv), Check: fastCheck(),
				}})
			}
		}(i)
	}
	c.Close()
	wg.Wait()
}
