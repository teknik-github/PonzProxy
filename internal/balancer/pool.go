package balancer

import (
	"errors"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

var (
	// ErrNoBackends means the host has no enabled upstream at all — a
	// configuration problem, reported to the client as 503.
	ErrNoBackends = errors.New("no enabled upstreams configured")
	// ErrNoHealthyBackend means every enabled upstream is down, ejected, or
	// already at its connection limit — an operational problem.
	ErrNoHealthyBackend = errors.New("no healthy upstream available")
)

// Pool is the live set of backends for one host together with its selection
// algorithm.
//
// A Pool is immutable once built. Configuration changes construct a new Pool
// and swap it in, so a request that has begun keeps the pool it started with
// and the request path never takes a lock on the pool itself.
type Pool struct {
	hostID    int64
	algorithm domain.Algorithm
	backends  []*Backend
	selector  Selector
}

// NewPool builds the pool for a host. When prev is non-nil, backends that
// address the same upstream inherit its health and counters, so editing an
// unrelated field does not blank the dashboard or re-probe a known-good
// backend from scratch.
func NewPool(hostID int64, algorithm domain.Algorithm, upstreams []domain.Upstream, prev *Pool) *Pool {
	var inherited map[string]*Backend
	if prev != nil {
		inherited = make(map[string]*Backend, len(prev.backends))
		for _, b := range prev.backends {
			inherited[b.key] = b
		}
	}

	backends := make([]*Backend, 0, len(upstreams))
	for _, u := range upstreams {
		b := NewBackend(u)
		if old, ok := inherited[b.key]; ok {
			b.inheritFrom(old)
		}
		backends = append(backends, b)
	}

	return &Pool{
		hostID:    hostID,
		algorithm: algorithm,
		backends:  backends,
		selector:  newSelector(algorithm),
	}
}

// HostID identifies the host this pool serves.
func (p *Pool) HostID() int64 { return p.hostID }

// Algorithm reports which selection algorithm is in force.
func (p *Pool) Algorithm() domain.Algorithm { return p.algorithm }

// Backends returns every backend, healthy or not. The slice is shared and must
// not be modified; it exists for the health checker and the metrics snapshot.
func (p *Pool) Backends() []*Backend { return p.backends }

// Pick chooses the backend for one request.
//
// exclude lists backends already tried and failed for this request, so a retry
// lands somewhere new instead of hitting the same dead upstream again. It is a
// slice rather than a map because it holds at most a couple of entries.
func (p *Pool) Pick(clientIP string, exclude []*Backend) (*Backend, error) {
	if len(p.backends) == 0 {
		return nil, ErrNoBackends
	}

	// The clock is read once rather than per backend: every candidate is
	// judged against the same instant, and Available is on the hot path.
	now := time.Now()

	// candidates is stack-friendly for the common small pool; the capacity
	// hint means at most one allocation for larger ones.
	candidates := make([]*Backend, 0, len(p.backends))
	enabled := 0
	for _, b := range p.backends {
		if !b.Upstream.Enabled {
			continue
		}
		enabled++
		if !b.Available(now) || !b.HasCapacity() || containsBackend(exclude, b) {
			continue
		}
		candidates = append(candidates, b)
	}

	if enabled == 0 {
		return nil, ErrNoBackends
	}
	if len(candidates) == 0 {
		// Every enabled backend is out. Falling back to an ejected one
		// would be worse than failing: the retry loop has already tried
		// the ones that were available, and an ejection means the last
		// few requests to that backend could not connect at all.
		return nil, ErrNoHealthyBackend
	}
	return p.selector.Select(candidates, clientIP), nil
}

// Snapshot renders every backend for the live UI feed.
func (p *Pool) Snapshot() []domain.UpstreamSnapshot {
	out := make([]domain.UpstreamSnapshot, 0, len(p.backends))
	for _, b := range p.backends {
		out = append(out, b.Snapshot())
	}
	return out
}

// HealthyCount reports how many enabled backends are currently serving, which
// the system snapshot surfaces as "3 / 4 upstreams". A backend ejected by
// passive health counts as down: it is not taking traffic either way.
func (p *Pool) HealthyCount() (up, total int) {
	now := time.Now()
	for _, b := range p.backends {
		if !b.Upstream.Enabled {
			continue
		}
		total++
		if b.Available(now) {
			up++
		}
	}
	return up, total
}

func containsBackend(list []*Backend, b *Backend) bool {
	for _, x := range list {
		if x == b {
			return true
		}
	}
	return false
}
