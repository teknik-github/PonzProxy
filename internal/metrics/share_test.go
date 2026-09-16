package metrics

import (
	"testing"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// fakePools is a PoolProvider whose cumulative per-backend totals the test
// drives directly, standing in for the proxy engine.
type fakePools struct {
	hostID    int64
	addresses []string
	totals    []uint64
	// hidden drops a backend from the reported pool without resetting its
	// total, which is what a disabled or deleted upstream looks like here.
	hidden map[string]bool
}

func newFakePools(hostID int64, addresses ...string) *fakePools {
	return &fakePools{
		hostID:    hostID,
		addresses: addresses,
		totals:    make([]uint64, len(addresses)),
		hidden:    map[string]bool{},
	}
}

func (f *fakePools) MetricsPools() []PoolInfo {
	ups := make([]domain.UpstreamSnapshot, 0, len(f.addresses))
	for i, addr := range f.addresses {
		if f.hidden[addr] {
			continue
		}
		ups = append(ups, domain.UpstreamSnapshot{
			Address: addr, TotalRequests: f.totals[i], Healthy: true, Enabled: true,
		})
	}
	return []PoolInfo{{HostID: f.hostID, Name: "host", Enabled: true, Upstreams: ups}}
}

// serve adds n requests to each backend in order, as one interval's traffic.
func (f *fakePools) serve(n ...uint64) {
	for i := range n {
		f.totals[i] += n[i]
	}
}

// windows returns the rolling counts the collector would publish.
func windows(t *testing.T, c *Collector) map[string]uint64 {
	t.Helper()
	out := map[string]uint64{}
	for _, h := range c.Snapshot().Hosts {
		for _, u := range h.Upstreams {
			out[u.Address] = u.WindowRequests
		}
	}
	return out
}

// tick advances the sampler once, the way Run does.
func tick(c *Collector) { c.sampleShares(c.poolInfo()) }

func TestShareWindowFollowsAnAlgorithmChange(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	pools := newFakePools(1, "a", "b", "c")
	c.SetPoolProvider(pools)

	// A long stretch of 5:2:1, long enough that the cumulative average is
	// firmly anchored to it.
	tick(c)
	for range 200 {
		pools.serve(50, 20, 10)
		tick(c)
	}

	// The operator switches the host to an even split. Every interval from
	// here on is 1:1:1.
	for range shareBuckets {
		pools.serve(20, 20, 20)
		tick(c)
	}

	w := windows(t, c)
	if w["a"] != w["b"] || w["b"] != w["c"] {
		t.Errorf("one window of even traffic should be even, got a=%d b=%d c=%d",
			w["a"], w["b"], w["c"])
	}

	// The cumulative totals still say 5:2:1, which is the whole point: the
	// window is reporting something the totals cannot.
	snap := c.Snapshot()
	byAddr := map[string]uint64{}
	for _, u := range snap.Hosts[0].Upstreams {
		byAddr[u.Address] = u.TotalRequests
	}
	if byAddr["a"] <= 2*byAddr["c"] {
		t.Fatalf("test is not exercising the difference: cumulative a=%d c=%d",
			byAddr["a"], byAddr["c"])
	}
}

func TestShareWindowMovesBeforeItHasFullyTurnedOver(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	pools := newFakePools(1, "a", "b")
	c.SetPoolProvider(pools)

	tick(c)
	for range shareBuckets {
		pools.serve(90, 10)
		tick(c)
	}
	before := windows(t, c)
	if got := shareOf(before, "b"); got > 0.11 {
		t.Fatalf("b started at %.2f of the traffic, want ~0.10", got)
	}

	// Half a window of the opposite split. An operator watching should see
	// the diagram move well before the old traffic has aged out entirely,
	// otherwise the change still looks like it did nothing.
	for range shareBuckets / 2 {
		pools.serve(10, 90)
		tick(c)
	}

	// Exactly halfway the two are tied, because half the buckets hold each
	// split — so this asserts the midpoint rather than a lead. Getting from
	// 10% to 50% in fifteen seconds is the movement that matters; the lead
	// follows on the next tick.
	if got := shareOf(windows(t, c), "b"); got < 0.49 || got > 0.51 {
		t.Errorf("after half a window b holds %.2f of the traffic, want ~0.50", got)
	}

	pools.serve(10, 90)
	tick(c)
	if got := shareOf(windows(t, c), "b"); got <= 0.5 {
		t.Errorf("one tick past the midpoint b holds %.2f, want more than half", got)
	}
}

// shareOf is the fraction of a host's windowed traffic one backend holds —
// the number the diagram turns into a line width.
func shareOf(w map[string]uint64, addr string) float64 {
	var total uint64
	for _, v := range w {
		total += v
	}
	if total == 0 {
		return 0
	}
	return float64(w[addr]) / float64(total)
}

func TestShareWindowForgetsTrafficOlderThanTheWindow(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	pools := newFakePools(1, "a")
	c.SetPoolProvider(pools)

	tick(c)
	pools.serve(100)
	tick(c)
	if got := windows(t, c)["a"]; got != 100 {
		t.Fatalf("window = %d, want 100", got)
	}

	// Silence for a full window. The burst must age out rather than sit on
	// the diagram forever.
	for range shareBuckets {
		tick(c)
	}
	if got := windows(t, c)["a"]; got != 0 {
		t.Errorf("window after %v of silence = %d, want 0", shareWindow, got)
	}
}

func TestShareWindowIgnoresHistoryOnTheFirstReading(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	pools := newFakePools(1, "a")
	// The backend already carries a large cumulative total when the
	// collector first sees it, which is what a reload looks like. That
	// history is not traffic from this interval.
	pools.totals[0] = 9_000
	c.SetPoolProvider(pools)

	tick(c)
	if got := windows(t, c)["a"]; got != 0 {
		t.Errorf("first reading contributed %d to the window, want 0", got)
	}

	pools.serve(7)
	tick(c)
	if got := windows(t, c)["a"]; got != 7 {
		t.Errorf("window = %d, want 7", got)
	}
}

func TestShareWindowSurvivesABackendMissingOneTick(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	pools := newFakePools(1, "a", "b")
	c.SetPoolProvider(pools)

	tick(c)
	pools.serve(10, 10)
	tick(c)

	// A configuration reload can briefly drop a backend from the reported
	// pool. Losing its window on the first miss would blank the diagram on
	// every edit.
	pools.hidden["b"] = true
	tick(c)
	pools.hidden["b"] = false

	if got := windows(t, c)["b"]; got != 10 {
		t.Errorf("window after one missed tick = %d, want 10", got)
	}
}

func TestShareWindowDropsBackendsThatStayGone(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	pools := newFakePools(1, "a", "b")
	c.SetPoolProvider(pools)

	tick(c)
	pools.serve(10, 10)
	tick(c)

	pools.hidden["b"] = true
	for range shareBuckets + staleTicks + 1 {
		tick(c)
	}

	c.mu.RLock()
	_, still := c.shares[shareKey{hostID: 1, address: "b"}]
	c.mu.RUnlock()
	if still {
		t.Error("a backend gone for a full window should not be retained")
	}
}

func TestSnapshotReportsTheShareWindowLength(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	if got, want := c.Snapshot().ShareWindowSeconds, int(shareWindow.Seconds()); got != want {
		t.Errorf("ShareWindowSeconds = %d, want %d", got, want)
	}
}

func TestShareWindowCountsARebuiltBackendFromScratch(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	pools := newFakePools(1, "a")
	c.SetPoolProvider(pools)

	tick(c)
	pools.serve(500)
	tick(c)

	// A pool rebuilt without inheriting counters reports a total lower than
	// the last reading. Treating that as a negative delta would underflow a
	// uint64 and put an absurd number on the diagram.
	pools.totals[0] = 3
	tick(c)

	if got := windows(t, c)["a"]; got != 503 {
		t.Errorf("window = %d, want 503 (500 + the rebuilt backend's 3)", got)
	}
}
