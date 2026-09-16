package metrics

import (
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// The share window: how much recent traffic decides the split the console
// draws, and how it is bucketed.
//
// Why a window at all. Backends count requests cumulatively since the process
// started, and a share computed from those totals is an average over the whole
// uptime. That average is almost right on a proxy whose configuration never
// changes, and badly wrong the moment it does: switching a host from weighted
// round robin to round robin takes effect on the very next request, but a pool
// carrying a few thousand historical requests keeps drawing the old weighting
// for many minutes afterwards. The one screen that should answer "did my
// change take?" was the slowest thing in the system to admit it had.
//
// Why 30 seconds. Short enough that an operator watching the diagram sees a
// configuration change land while they are still looking at it; long enough
// that the widths do not twitch. Per-second buckets are what make the window
// slide smoothly rather than resetting to zero every half minute.
const (
	shareWindow  = 30 * time.Second
	shareBuckets = int(shareWindow / rateInterval)

	// staleTicks is how many samples a backend may go unseen before its
	// window is dropped. A configuration reload rebuilds pools, so a
	// backend can miss a tick without having gone anywhere; discarding its
	// history on the first miss would blank the diagram on every edit.
	staleTicks = 3
)

// shareKey identifies a backend across configuration reloads. The address is
// used rather than the database id for the same reason the balancer keys its
// health state that way: an edit can reassign ids, and losing the window on an
// unrelated edit is exactly the staleness this file exists to remove.
type shareKey struct {
	hostID  int64
	address string
}

// shareWindowState is one backend's rolling count. It lives in the collector
// rather than on the Backend because it is built from readings the sampler
// already takes once a second: keeping it here costs the request path nothing,
// and the request path is the one place in this proxy where nothing is free.
type shareWindowState struct {
	// last is the previous cumulative reading, so each tick contributes the
	// delta since the one before it.
	last uint64
	// primed reports that last holds a real reading. The first tick after a
	// backend appears establishes a baseline and contributes nothing, since
	// its cumulative total is history, not traffic from this second.
	primed bool

	buckets [shareBuckets]uint64
	next    int
	// sum is the running total of buckets, maintained incrementally so a
	// read is a field access rather than a loop over the window.
	sum uint64

	missed int
}

// push folds one cumulative reading into the window.
func (w *shareWindowState) push(total uint64) {
	w.missed = 0

	if !w.primed {
		w.last = total
		w.primed = true
		return
	}

	// A total that went backwards means this is a different backend wearing
	// a familiar address — a pool rebuilt without inheriting counters. Its
	// whole total is then new traffic as far as this window is concerned.
	delta := total
	if total >= w.last {
		delta = total - w.last
	}
	w.last = total

	w.sum -= w.buckets[w.next]
	w.buckets[w.next] = delta
	w.sum += delta
	w.next = (w.next + 1) % shareBuckets
}

// idle advances the window by one empty bucket for a backend the sampler did
// not see. Without it a backend that stops reporting would keep its last
// window forever, and a deleted host's final burst would sit on the diagram
// indefinitely.
func (w *shareWindowState) idle() {
	w.missed++
	w.sum -= w.buckets[w.next]
	w.buckets[w.next] = 0
	w.next = (w.next + 1) % shareBuckets
}

// sampleShares advances every backend's rolling window by one bucket. It runs
// on the same ticker as sampleRates, and for the same reason: one consumer
// advancing the window means two dashboards watching cannot split an interval
// between them.
func (c *Collector) sampleShares(pools []PoolInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()

	seen := make(map[shareKey]struct{}, len(c.shares))
	for _, p := range pools {
		for _, u := range p.Upstreams {
			key := shareKey{hostID: p.HostID, address: u.Address}
			seen[key] = struct{}{}

			w := c.shares[key]
			if w == nil {
				w = &shareWindowState{}
				c.shares[key] = w
			}
			w.push(u.TotalRequests)
		}
	}

	for key, w := range c.shares {
		if _, ok := seen[key]; ok {
			continue
		}
		w.idle()
		// Only a backend that has been gone for several ticks and has no
		// traffic left in its window is forgotten. Dropping it while the
		// window still holds requests would make those requests vanish
		// from the split rather than decay out of it.
		if w.missed >= staleTicks && w.sum == 0 {
			delete(c.shares, key)
		}
	}
}

// applyShares fills in WindowRequests for one host's upstreams. The caller
// holds at least a read lock.
func (c *Collector) applyShares(hostID int64, upstreams []domain.UpstreamSnapshot) {
	for i := range upstreams {
		if w := c.shares[shareKey{hostID: hostID, address: upstreams[i].Address}]; w != nil {
			upstreams[i].WindowRequests = w.sum
		}
	}
}
