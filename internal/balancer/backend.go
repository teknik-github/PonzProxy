// Package balancer turns a host's configured upstreams into a live pool and
// decides which backend serves the next request. It holds all the mutable
// per-backend state — health, in-flight count, counters — that configuration
// alone cannot express.
package balancer

import (
	"hash/maphash"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// hashSeed keeps IP-hash routing stable for the lifetime of the process while
// differing between processes, so an unlucky client-to-backend distribution
// does not reproduce itself after a restart.
var hashSeed = maphash.MakeSeed()

// Backend is one upstream plus everything the proxy learns about it at
// runtime. Every mutable field is atomic: the request path reads them without
// a lock, and the health checker writes them from its own goroutine.
type Backend struct {
	// Upstream is the configuration this backend was built from. It is
	// never mutated; a config change builds a new Backend instead.
	Upstream domain.Upstream
	// URL is the pre-parsed base address, so the hot path does no parsing.
	URL *url.URL
	// key identifies the backend across config reloads, letting health
	// state survive an edit that reassigns database ids.
	key string
	// keyHash is key hashed once at construction, so IPHash does no string
	// hashing per backend per request.
	keyHash uint64

	healthy       atomic.Bool
	activeConns   atomic.Int64
	totalRequests atomic.Uint64
	totalLatency  atomic.Uint64 // milliseconds, summed
	lastError     atomic.Pointer[string]

	// probeMu guards the streak counters below. They are only touched by
	// health probes, but a configuration reload can briefly overlap the
	// outgoing and incoming checker goroutines for a host.
	probeMu         sync.Mutex
	consecutiveOK   int
	consecutiveFail int

	// ejectedUntil is the unix nano deadline set by passive health, or 0
	// when the backend is not ejected. It is separate from healthy because
	// the two have different causes: healthy reflects the active probe,
	// this reflects real traffic failing. An operator needs to see which
	// of the two removed a backend.
	ejectedUntil atomic.Int64
	// proxyFails counts consecutive connection failures on real requests.
	proxyFails atomic.Int64

	// currentWeight is smooth weighted round robin's running score. It is
	// guarded by the weighted selector's mutex, not by this struct.
	currentWeight int
}

// NewBackend builds a backend for an upstream. It starts healthy: refusing
// traffic until the first probe succeeds would create an outage on every
// restart, so the checker is trusted to take it down if it is in fact broken.
func NewBackend(u domain.Upstream) *Backend {
	key := backendKey(u)
	b := &Backend{
		Upstream: u,
		URL:      u.URL(),
		key:      key,
		keyHash:  maphash.String(hashSeed, key),
	}
	b.healthy.Store(true)
	return b
}

// backendKey identifies a backend by what it actually addresses, so renaming
// or reordering upstreams does not reset their health.
func backendKey(u domain.Upstream) string { return u.Scheme + "://" + u.Address }

// Key returns the stable identity used to carry state across reloads.
func (b *Backend) Key() string { return b.key }

// Healthy reports whether the active probe considers the backend up. It says
// nothing about passive ejection; use Available for the question the request
// path actually asks.
func (b *Backend) Healthy() bool { return b.healthy.Load() }

// Ejected reports whether passive health currently has the backend out of
// rotation, and how much longer for.
func (b *Backend) Ejected(now time.Time) (ejected bool, remaining time.Duration) {
	until := b.ejectedUntil.Load()
	if until == 0 {
		return false, 0
	}
	left := time.Duration(until - now.UnixNano())
	if left <= 0 {
		return false, 0
	}
	return true, left
}

// Available reports whether the backend may serve a request right now: the
// active probe has not taken it down, and passive health has not ejected it.
func (b *Backend) Available(now time.Time) bool {
	if !b.healthy.Load() {
		return false
	}
	ejected, _ := b.Ejected(now)
	return !ejected
}

// RecordProxyResult folds the outcome of one real request into passive health.
//
// Only connection-level failures should be reported as a failure: a backend
// that answers 500 is alive, and may be answering 500 because the request
// deserved it. Ejecting on upstream status codes would let one bad client
// take a healthy backend out of rotation.
//
// When the ejection window lapses, traffic returns to the backend and the next
// run of failures ejects it again. That is deliberately simpler than holding a
// single trial request back: at MaxFails=3 the cost of a still-dead backend is
// three failures per window, and the retry loop already moves those requests
// to another backend.
//
// It reports whether the ejection state changed, so the caller only logs a
// transition.
func (b *Backend) RecordProxyResult(success bool, cfg domain.PassiveHealth, now time.Time) (changed bool) {
	if !cfg.Enabled {
		return false
	}

	if success {
		b.proxyFails.Store(0)
		// A success inside the window clears the ejection early: the
		// backend is evidently answering again.
		return b.ejectedUntil.Swap(0) != 0
	}

	if fails := b.proxyFails.Add(1); fails < int64(cfg.MaxFails) {
		return false
	}
	b.proxyFails.Store(0)

	deadline := now.Add(cfg.EjectFor).UnixNano()
	// Only report a change when the backend was not already ejected, so a
	// burst of failures produces one log line rather than one per request.
	wasEjected, _ := b.Ejected(now)
	b.ejectedUntil.Store(deadline)
	return !wasEjected
}

// ClearEjection puts an ejected backend back into rotation immediately. The
// active health checker calls it on a successful probe, so a backend that
// recovers is not held out for the rest of its window.
func (b *Backend) ClearEjection() {
	b.proxyFails.Store(0)
	b.ejectedUntil.Store(0)
}

// SetHealthy flips health state and reports whether it changed, so callers
// only log and broadcast on an actual transition.
func (b *Backend) SetHealthy(v bool) bool { return b.healthy.Swap(v) != v }

// ActiveConns is the number of requests in flight, which is what
// LeastConnections selects on.
func (b *Backend) ActiveConns() int64 { return b.activeConns.Load() }

// Acquire marks the start of a proxied request.
func (b *Backend) Acquire() { b.activeConns.Add(1) }

// Release marks the end of one and records its latency.
func (b *Backend) Release(latency time.Duration) {
	b.activeConns.Add(-1)
	b.totalRequests.Add(1)
	b.totalLatency.Add(uint64(latency.Milliseconds()))
}

// HasCapacity reports whether the backend is below its MaxConns limit. A
// MaxConns of 0 means unlimited.
func (b *Backend) HasCapacity() bool {
	limit := b.Upstream.MaxConns
	return limit == 0 || b.activeConns.Load() < int64(limit)
}

// RecordError stores the most recent failure so the UI can explain why a
// backend was taken out of rotation.
func (b *Backend) RecordError(err error) {
	if err == nil {
		b.lastError.Store(nil)
		return
	}
	msg := err.Error()
	b.lastError.Store(&msg)
}

// LastError returns the stored failure, or "" when the backend is clean.
func (b *Backend) LastError() string {
	if p := b.lastError.Load(); p != nil {
		return *p
	}
	return ""
}

// Snapshot renders the backend for the live UI feed.
func (b *Backend) Snapshot() domain.UpstreamSnapshot {
	total := b.totalRequests.Load()
	var mean float64
	if total > 0 {
		mean = float64(b.totalLatency.Load()) / float64(total)
	}
	ejected, remaining := b.Ejected(time.Now())
	return domain.UpstreamSnapshot{
		Ejected:           ejected,
		EjectedForSeconds: int64(remaining.Seconds()),
		UpstreamID:        b.Upstream.ID,
		Address:           b.key,
		Healthy:           b.Healthy(),
		Enabled:           b.Upstream.Enabled,
		Weight:            b.Upstream.Weight,
		ActiveConns:       b.ActiveConns(),
		TotalRequests:     total,
		MeanLatencyMS:     mean,
		LastError:         b.LastError(),
	}
}

// RecordProbe folds one health probe result into the backend's streak
// counters and flips its health when a threshold is crossed.
//
// Thresholds exist so a single blip does not eject a backend and a single
// lucky response does not bring a flapping one back. Streaks are kept here,
// next to the health flag they control, rather than in the checker, so they
// survive a configuration reload along with the rest of the backend's state.
//
// It reports whether the health state actually changed, so the caller only
// logs and broadcasts on a real transition.
func (b *Backend) RecordProbe(success bool, healthyThreshold, unhealthyThreshold int) (changed bool) {
	b.probeMu.Lock()
	defer b.probeMu.Unlock()

	if success {
		b.consecutiveFail = 0
		b.consecutiveOK++
		if !b.Healthy() && b.consecutiveOK >= healthyThreshold {
			b.RecordError(nil)
			return b.SetHealthy(true)
		}
		return false
	}

	b.consecutiveOK = 0
	b.consecutiveFail++
	if b.Healthy() && b.consecutiveFail >= unhealthyThreshold {
		return b.SetHealthy(false)
	}
	return false
}

// inheritFrom copies runtime state from the backend this one replaces, so a
// configuration edit does not reset health or lose counters.
func (b *Backend) inheritFrom(old *Backend) {
	b.healthy.Store(old.healthy.Load())
	b.totalRequests.Store(old.totalRequests.Load())
	b.totalLatency.Store(old.totalLatency.Load())
	b.lastError.Store(old.lastError.Load())
	// Ejection carries across a reload for the same reason health does: an
	// unrelated configuration edit must not hand traffic back to a backend
	// that is still refusing connections.
	b.ejectedUntil.Store(old.ejectedUntil.Load())
	b.proxyFails.Store(old.proxyFails.Load())
	old.probeMu.Lock()
	b.consecutiveOK, b.consecutiveFail = old.consecutiveOK, old.consecutiveFail
	old.probeMu.Unlock()
	// activeConns deliberately starts at zero: in-flight requests are still
	// counted against the old backend, which stays alive until they finish.
}
