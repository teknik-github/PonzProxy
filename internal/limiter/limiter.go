// Package limiter decides whether one client address may make another request
// of a host right now.
//
// It is deliberately not a general-purpose rate limiter. It runs on the
// request path of a reverse proxy, which sets two constraints that shape
// everything here: it must cost far less than the work it protects, and it
// must not grow without bound when the addresses it is tracking are chosen by
// someone hostile.
package limiter

import (
	"hash/maphash"
	"net"
	"sync"
	"time"
)

// Verdict is what the limiter decided, and why. The reason is separate from
// the decision because detect mode reports a verdict it does not act on, and
// "which limit would have fired" is the whole value of running in detect.
type Verdict struct {
	// Limited is true when a limit was exceeded, whether or not the caller
	// intends to enforce it.
	Limited bool
	Reason  Reason
	// RetryAfter is how long the client should wait, for the header of the
	// same name. Zero when the limit is not a rate.
	RetryAfter time.Duration
}

type Reason string

const (
	ReasonNone Reason = ""
	// ReasonRate is the sustained request rate.
	ReasonRate Reason = "rate"
	// ReasonConcurrency is too many requests in flight at once.
	ReasonConcurrency Reason = "concurrency"
	// ReasonBodySize is a request body larger than the host allows.
	ReasonBodySize Reason = "body_size"
)

// Rules is one host's limits, as the limiter needs them. It is a value rather
// than a pointer into configuration so that a reload cannot change the numbers
// underneath a request already being judged.
type Rules struct {
	RequestsPerSecond int
	Burst             int
	MaxConcurrent     int
}

// Tunables of the bookkeeping, as opposed to the limits themselves.
const (
	// shards splits the address table so that concurrent requests from
	// different clients rarely wait on each other. 64 is enough that lock
	// contention disappears well past the rates this proxy can serve, and
	// small enough that a sweep is cheap.
	shards = 64

	// idleAfter is how long an address with nothing in flight is kept. It
	// must exceed the time it takes a full bucket to refill, or a client
	// could drop its record and reappear with a fresh burst.
	idleAfter = 10 * time.Minute

	// sweepEvery is how often expired records are discarded.
	sweepEvery = time.Minute

	// maxPerShard bounds memory against a very large number of distinct
	// addresses. Addresses cannot be spoofed on an established TCP
	// connection, so this is a rail rather than a defence; reaching it
	// means the proxy genuinely has that many clients.
	maxPerShard = 16_384
)

// client is one address's state for one host.
type client struct {
	// tokens is the bucket level, in requests.
	tokens float64
	// last is when tokens was last refilled.
	last time.Time
	// active is requests in flight. A record is never evicted while this
	// is above zero, or releasing would decrement a record that no longer
	// exists and the count would drift down forever.
	active int
	// seen is the last time this record was touched, for the sweep.
	seen time.Time
}

type shard struct {
	mu      sync.Mutex
	clients map[key]*client
}

// key identifies one client of one host. Limits are per host, so the same
// address hitting two hosts gets two budgets — which is what an operator means
// by "50 requests per second for this site".
//
// The address is a fixed array rather than a string because this is built on
// every request: net.IP.String() formats and allocates, which measured as half
// the cost of the whole check.
type key struct {
	hostID int64
	addr   [16]byte
}

// addrKey packs an address into the key's fixed array without allocating.
// To4 returns a sub-slice of its receiver for both 4-byte and v4-mapped
// 16-byte addresses, so neither form allocates here.
func addrKey(ip net.IP) [16]byte {
	var a [16]byte
	if v4 := ip.To4(); v4 != nil {
		// The v4-mapped prefix keeps 1.2.3.4 and ::ffff:1.2.3.4 as one
		// client rather than two budgets for the same visitor.
		a[10], a[11] = 0xff, 0xff
		copy(a[12:], v4)
		return a
	}
	copy(a[:], ip)
	return a
}

// Limiter is safe for concurrent use and must be closed to stop its sweeper.
type Limiter struct {
	seed   maphash.Seed
	shards [shards]shard

	stop chan struct{}
	once sync.Once

	// now is injectable so tests do not have to sleep out a window.
	now func() time.Time
}

// New builds a limiter and starts its sweeper.
func New() *Limiter {
	l := &Limiter{
		seed: maphash.MakeSeed(),
		stop: make(chan struct{}),
		now:  time.Now,
	}
	for i := range l.shards {
		l.shards[i].clients = make(map[key]*client)
	}
	go l.sweep()
	return l
}

// Close stops the sweeper. It is safe to call more than once.
func (l *Limiter) Close() {
	l.once.Do(func() { close(l.stop) })
}

// Release returns a slot taken by Acquire. The zero value is safe to call, so
// a caller can defer it without first checking whether anything was taken.
type Release func()

// Acquire judges one request and, when it is allowed, takes a concurrency slot
// that the returned Release gives back.
//
// A verdict that is Limited still returns a Release, and that Release is a
// no-op: detect mode forwards a request it has judged, and it must not be
// counted against a concurrency budget it was already over.
func (l *Limiter) Acquire(hostID int64, addr net.IP, rules Rules) (Verdict, Release) {
	if addr == nil {
		// Without an address there is nobody to limit. Refusing would turn
		// an unparseable RemoteAddr into an outage.
		return Verdict{}, func() {}
	}

	k := key{hostID: hostID, addr: addrKey(addr)}
	sh := &l.shards[l.shardOf(k)]
	now := l.now()

	sh.mu.Lock()
	c := sh.clients[k]
	if c == nil {
		if len(sh.clients) >= maxPerShard {
			// Dropping the oldest idle record is better than refusing a
			// client because the table is full: the table being full is
			// the proxy's problem, not the visitor's.
			l.evictOldest(sh)
		}
		c = &client{tokens: float64(burstOf(rules)), last: now}
		sh.clients[k] = c
	}
	c.seen = now

	if v := judge(c, rules, now); v.Limited {
		sh.mu.Unlock()
		return v, func() {}
	}

	c.active++
	sh.mu.Unlock()

	var once sync.Once
	return Verdict{}, func() {
		once.Do(func() {
			sh.mu.Lock()
			if cur := sh.clients[k]; cur != nil && cur.active > 0 {
				cur.active--
			}
			sh.mu.Unlock()
		})
	}
}

// judge applies the rules to a record the caller already holds the lock for.
// It consumes a token only when the request is allowed: a refused request must
// not dig the client deeper into a hole it is already in, or a client hammering
// a limit would never recover.
func judge(c *client, rules Rules, now time.Time) Verdict {
	if rules.MaxConcurrent > 0 && c.active >= rules.MaxConcurrent {
		return Verdict{Limited: true, Reason: ReasonConcurrency}
	}

	if rules.RequestsPerSecond <= 0 {
		return Verdict{}
	}

	rate := float64(rules.RequestsPerSecond)
	burst := float64(burstOf(rules))

	elapsed := now.Sub(c.last).Seconds()
	if elapsed > 0 {
		c.tokens += elapsed * rate
		if c.tokens > burst {
			c.tokens = burst
		}
		c.last = now
	}

	if c.tokens < 1 {
		// How long until one token exists. Rounded up to a whole second
		// because Retry-After is expressed in seconds and rounding down
		// would invite the client straight back into another refusal.
		wait := time.Duration(((1 - c.tokens) / rate) * float64(time.Second))
		if wait < time.Second {
			wait = time.Second
		}
		return Verdict{Limited: true, Reason: ReasonRate, RetryAfter: wait}
	}

	c.tokens--
	return Verdict{}
}

func burstOf(rules Rules) int {
	if rules.Burst > rules.RequestsPerSecond {
		return rules.Burst
	}
	if rules.RequestsPerSecond > 0 {
		return rules.RequestsPerSecond
	}
	return 1
}

// shardOf hashes the whole key at once. maphash.Comparable does it without
// serialising anything, which is the point: the previous version wrote the key
// through a maphash.Hash and paid for the copy on every request.
func (l *Limiter) shardOf(k key) uint64 {
	return maphash.Comparable(l.seed, k) % shards
}

// evictOldest drops the least recently seen idle record. The caller holds the
// shard lock.
func (l *Limiter) evictOldest(sh *shard) {
	var (
		oldestKey key
		oldest    time.Time
		found     bool
	)
	for k, c := range sh.clients {
		if c.active > 0 {
			continue
		}
		if !found || c.seen.Before(oldest) {
			oldestKey, oldest, found = k, c.seen, true
		}
	}
	if found {
		delete(sh.clients, oldestKey)
	}
}

func (l *Limiter) sweep() {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.sweepOnce()
		}
	}
}

func (l *Limiter) sweepOnce() {
	cutoff := l.now().Add(-idleAfter)
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		for k, c := range sh.clients {
			if c.active == 0 && c.seen.Before(cutoff) {
				delete(sh.clients, k)
			}
		}
		sh.mu.Unlock()
	}
}

// Forget drops every record for a host. A host whose limits were switched off,
// or which was deleted, should not leave its clients' budgets behind to be
// applied again if it comes back.
func (l *Limiter) Forget(hostID int64) {
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		for k := range sh.clients {
			if k.hostID == hostID {
				delete(sh.clients, k)
			}
		}
		sh.mu.Unlock()
	}
}

// Tracked reports how many client records exist, for tests and diagnostics.
func (l *Limiter) Tracked() int {
	n := 0
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		n += len(sh.clients)
		sh.mu.Unlock()
	}
	return n
}

// shardsForTest visits every client record. It exists for the tests, which
// need to assert on bookkeeping the public surface deliberately hides.
func (l *Limiter) shardsForTest(fn func(*client)) {
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		for _, c := range sh.clients {
			fn(c)
		}
		sh.mu.Unlock()
	}
}
