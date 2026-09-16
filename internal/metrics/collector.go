// Package metrics accumulates traffic counters on the request path and turns
// them into two things: a live snapshot for the UI, and periodic samples
// persisted for the historical charts.
package metrics

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// UnmatchedHostID is the bucket for requests that matched no configured host.
// Without it, traffic to an unknown domain would vanish from the dashboard,
// which is exactly the traffic an operator most wants to see.
const UnmatchedHostID int64 = 0

// counters are the raw, monotonically increasing totals for one host. They are
// written by every proxied request, so each field is atomic and the struct is
// never copied.
type counters struct {
	requests atomic.Uint64
	status   [5]atomic.Uint64
	bytesIn  atomic.Uint64
	bytesOut atomic.Uint64
	latSum   atomic.Uint64 // milliseconds
	latMax   atomic.Uint64
	active   atomic.Int64
	// limited counts requests that exceeded a traffic limit, and blocked
	// how many of those were actually refused. They are separate because
	// detect mode's entire purpose is the gap between the two: an operator
	// needs to see what enforcement would have cost before enabling it.
	limited atomic.Uint64
	blocked atomic.Uint64
}

// totals is a plain-value reading of counters, used as a baseline for deltas.
type totals struct {
	requests uint64
	status   [5]uint64
	bytesIn  uint64
	bytesOut uint64
	latSum   uint64
	latMax   uint64
}

func (c *counters) read() totals {
	t := totals{
		requests: c.requests.Load(),
		bytesIn:  c.bytesIn.Load(),
		bytesOut: c.bytesOut.Load(),
		latSum:   c.latSum.Load(),
		latMax:   c.latMax.Load(),
	}
	for i := range c.status {
		t.status[i] = c.status[i].Load()
	}
	return t
}

// sub returns the traffic that happened between two readings.
func (t totals) sub(base totals) totals {
	out := totals{
		requests: t.requests - base.requests,
		bytesIn:  t.bytesIn - base.bytesIn,
		bytesOut: t.bytesOut - base.bytesOut,
		latSum:   t.latSum - base.latSum,
		latMax:   t.latMax, // a running maximum, not a delta
	}
	for i := range t.status {
		out.status[i] = t.status[i] - base.status[i]
	}
	return out
}

// hostState pairs a host's counters with the baselines of each consumer. The
// flush loop and the rate sampler read at different cadences, so each keeps
// its own baseline instead of resetting shared counters and racing the other.
type hostState struct {
	counters counters
	flushAt  totals

	// liveAt and liveTime belong solely to sampleRates. Nothing else may
	// advance them: a second consumer doing so would leave whichever ran
	// later dividing a near-empty delta by a near-zero interval, which
	// reports either zero or a rate hundreds of times too high.
	liveAt   totals
	liveTime time.Time

	// live is the most recently computed rate for this host. Readers take a
	// copy of it, so reading a snapshot has no effect on the next one.
	live domain.TrafficSnapshot
}

// PoolProvider supplies the current pools so a snapshot can report per-backend
// state. It is satisfied by the proxy engine; declaring it here keeps metrics
// independent of the proxy package.
type PoolProvider interface {
	// MetricsPools returns the live pools keyed by host id, along with each
	// host's display name.
	MetricsPools() []PoolInfo
}

// PoolInfo is one host's live pool as seen by the metrics snapshot.
type PoolInfo struct {
	HostID    int64
	Name      string
	Enabled   bool
	Upstreams []domain.UpstreamSnapshot
	Up, Total int
}

// Collector is the single sink for request outcomes.
type Collector struct {
	repo   domain.MetricsRepository
	logger *slog.Logger
	pools  PoolProvider

	startedAt time.Time
	cpu       cpuSampler

	mu    sync.RWMutex
	hosts map[int64]*hostState
	// shares holds each backend's rolling request count, advanced by the
	// same ticker as the rates above. See share.go.
	shares map[shareKey]*shareWindowState
	// totals is the combined rate across every host, computed alongside the
	// per-host rates so the two can never disagree.
	totals domain.TrafficSnapshot
}

// New builds a collector. pools may be set later with SetPoolProvider, since
// the proxy engine and the collector are constructed in either order.
func New(repo domain.MetricsRepository, logger *slog.Logger) *Collector {
	return &Collector{
		repo:      repo,
		logger:    logger.With("component", "metrics"),
		startedAt: time.Now(),
		hosts:     make(map[int64]*hostState),
		shares:    make(map[shareKey]*shareWindowState),
	}
}

// poolInfo reads the live pools, or nil when no provider is wired yet.
func (c *Collector) poolInfo() []PoolInfo {
	if c.pools == nil {
		return nil
	}
	return c.pools.MetricsPools()
}

// SetPoolProvider wires in the source of live backend state.
func (c *Collector) SetPoolProvider(p PoolProvider) { c.pools = p }

// Result describes one finished request. The proxy fills it in and hands it
// over exactly once.
type Result struct {
	HostID   int64
	Status   int
	BytesIn  int64
	BytesOut int64
	Duration time.Duration
	// Failed marks a request that never reached an upstream, so it is
	// counted as an error rather than as whatever status the client saw.
	Failed bool
}

// Record folds one finished request into the host's counters.
func (c *Collector) Record(r Result) {
	st := c.state(r.HostID)

	st.counters.requests.Add(1)

	class := domain.ClassifyStatus(r.Status)
	if r.Failed {
		class = domain.StatusError
	}
	st.counters.status[class].Add(1)

	if r.BytesIn > 0 {
		st.counters.bytesIn.Add(uint64(r.BytesIn))
	}
	if r.BytesOut > 0 {
		st.counters.bytesOut.Add(uint64(r.BytesOut))
	}

	ms := uint64(r.Duration.Milliseconds())
	st.counters.latSum.Add(ms)
	storeMax(&st.counters.latMax, ms)
}

// RecordLimited notes a request that exceeded a host's traffic limits.
// blocked says whether it was actually refused or merely observed.
func (c *Collector) RecordLimited(hostID int64, blocked bool) {
	st := c.state(hostID)
	st.counters.limited.Add(1)
	if blocked {
		st.counters.blocked.Add(1)
	}
}

// RequestStarted and RequestFinished bracket a request so the dashboard can
// show concurrency, which averaged counters cannot convey.
func (c *Collector) RequestStarted(hostID int64) { c.state(hostID).counters.active.Add(1) }

// RequestFinished closes the bracket opened by RequestStarted.
func (c *Collector) RequestFinished(hostID int64) { c.state(hostID).counters.active.Add(-1) }

// state returns the counters for a host, creating them on first sight.
func (c *Collector) state(hostID int64) *hostState {
	c.mu.RLock()
	st, ok := c.hosts[hostID]
	c.mu.RUnlock()
	if ok {
		return st
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Another goroutine may have created it while the lock was being taken.
	if st, ok = c.hosts[hostID]; ok {
		return st
	}
	st = &hostState{liveTime: time.Now()}
	c.hosts[hostID] = st
	return st
}

// Forget drops counters for hosts that no longer exist, so a long-running
// process does not accumulate state for deleted configuration. The unmatched
// bucket is always kept.
func (c *Collector) Forget(keep map[int64]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.hosts {
		if id == UnmatchedHostID {
			continue
		}
		if _, ok := keep[id]; !ok {
			delete(c.hosts, id)
		}
	}
}

// storeMax raises a running maximum without a lock.
func storeMax(dst *atomic.Uint64, v uint64) {
	for {
		cur := dst.Load()
		if v <= cur || dst.CompareAndSwap(cur, v) {
			return
		}
	}
}
