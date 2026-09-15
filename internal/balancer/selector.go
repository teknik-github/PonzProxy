package balancer

import (
	"hash/maphash"
	"math"
	"sync"
	"sync/atomic"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// Selector implements one load balancing algorithm. Implementations must be
// safe for concurrent use: a single pool serves every request for its host.
//
// candidates is already filtered to backends that are enabled, healthy and
// under their connection limit, and is never empty.
type Selector interface {
	// Select returns the backend to use. clientIP is the resolved remote
	// address, used only by IPHash; other algorithms ignore it.
	Select(candidates []*Backend, clientIP string) *Backend
}

// newSelector builds the selector for an algorithm. An unknown algorithm falls
// back to round robin rather than failing, so a database row written by a
// newer version cannot take a host offline.
func newSelector(a domain.Algorithm) Selector {
	switch a {
	case domain.WeightedRoundRobin:
		return &weightedRoundRobin{}
	case domain.LeastConnections:
		return leastConnections{}
	case domain.IPHash:
		return ipHash{}
	default:
		return &roundRobin{}
	}
}

// roundRobin cycles through candidates in order.
//
// The counter is shared across the whole pool rather than reset per call, so
// the rotation continues across requests. Because unhealthy backends are
// filtered out before Select, the cursor is taken modulo the *current*
// candidate count; the rotation therefore stays even as backends come and go,
// at the cost of a one-off shift when the set changes.
type roundRobin struct {
	counter atomic.Uint64
}

func (r *roundRobin) Select(candidates []*Backend, _ string) *Backend {
	// Add returns the post-increment value, so subtract one to make the
	// first request land on index 0.
	n := r.counter.Add(1) - 1
	return candidates[n%uint64(len(candidates))]
}

// weightedRoundRobin implements smooth weighted round robin, the algorithm
// nginx uses. Each pass adds every candidate's weight to its running score,
// picks the highest, then subtracts the total weight from the winner.
//
// For weights 5/1/1 this yields A B A C A A B A C A rather than five
// consecutive A's, which keeps latency even instead of bursty.
type weightedRoundRobin struct {
	mu sync.Mutex
}

func (w *weightedRoundRobin) Select(candidates []*Backend, _ string) *Backend {
	// currentWeight is per-backend mutable state, so unlike the other
	// selectors this one needs a lock. It is held for a handful of
	// arithmetic operations over a list that is almost always tiny.
	w.mu.Lock()
	defer w.mu.Unlock()

	var (
		best  *Backend
		total int
	)
	for _, b := range candidates {
		weight := b.Upstream.Weight
		total += weight
		b.currentWeight += weight
		if best == nil || b.currentWeight > best.currentWeight {
			best = b
		}
	}
	best.currentWeight -= total
	return best
}

// leastConnections picks the backend with the fewest requests in flight.
//
// Ties are broken by weight, so a backend configured to take twice the load
// wins an otherwise equal comparison. The scan is O(n) with no allocation and
// no lock, which beats maintaining a heap for the handful of backends a
// single host normally has.
type leastConnections struct{}

func (leastConnections) Select(candidates []*Backend, _ string) *Backend {
	best := candidates[0]
	bestConns := best.ActiveConns()

	for _, b := range candidates[1:] {
		conns := b.ActiveConns()
		switch {
		case conns < bestConns:
			best, bestConns = b, conns
		case conns == bestConns && b.Upstream.Weight > best.Upstream.Weight:
			best = b
		}
	}
	return best
}

// ipHash gives a client a stable backend without cookies.
//
// It uses weighted rendezvous hashing rather than `hash(ip) % len(backends)`.
// Modulo reshuffles nearly every client when one backend leaves the pool,
// which defeats the point of sticky sessions during a rolling restart;
// rendezvous hashing only moves the clients that were pinned to the departed
// backend. Weights are honoured through the standard score formula, so a
// backend with weight 2 attracts roughly twice the clients.
type ipHash struct{}

func (h ipHash) Select(candidates []*Backend, clientIP string) *Backend {
	// The client address is hashed once, then combined with each backend's
	// precomputed key hash. Feeding both through one incremental hash
	// instead would leave the per-backend scores correlated — they would
	// share every byte of internal state up to the suffix — and the weight
	// ratios came out badly skewed in practice.
	ipHashed := maphash.String(hashSeed, clientIP)

	var (
		best      *Backend
		bestScore = math.Inf(-1)
	)
	for _, b := range candidates {
		if score := weightedScore(ipHashed, b); score > bestScore {
			best, bestScore = b, score
		}
	}
	return best
}

// weightedScore is weight / -ln(u), where u is this (client, backend) pair
// mapped uniformly into (0,1). Since -ln(u)/weight is exponentially
// distributed with rate `weight`, taking the maximum score selects each
// backend with probability proportional to its weight.
func weightedScore(ipHashed uint64, b *Backend) float64 {
	// Map to (0,1) using the top 53 bits — the most significant ones, and
	// exactly what a float64 can represent without rounding. Adding one to
	// both terms keeps u strictly inside the open interval so the logarithm
	// stays finite at either end.
	u := (float64(mix64(ipHashed, b.keyHash)>>11) + 1) / (float64(uint64(1)<<53) + 1)
	return float64(b.Upstream.Weight) / -math.Log(u)
}

// mix64 combines two hashes into one well-distributed value: a hash_combine
// step to fold them together, then the splitmix64 finalizer to avalanche the
// result so nearby inputs share no structure.
func mix64(a, b uint64) uint64 {
	x := a ^ (b + 0x9E3779B97F4A7C15 + (a << 6) + (a >> 2))
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	x ^= x >> 31
	return x
}
