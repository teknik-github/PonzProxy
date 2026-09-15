// Package cache holds cacheable upstream responses in memory so the proxy can
// answer repeat requests for static assets without touching a backend.
//
// Three properties drive the whole design.
//
// Memory is bounded twice. Every host has a byte budget and a per-object size
// limit, and nothing is ever admitted that would breach either. A cache in a
// proxy without an upper bound is an out-of-memory failure waiting for enough
// distinct URLs, and the URLs are chosen by whoever is sending the requests.
//
// Entries are immutable once stored. A lookup takes the host's lock only long
// enough to find the entry and move it to the front of the recency list; the
// caller then reads the body with no lock at all, because nothing can ever
// modify it. Eviction unlinks an entry without touching it, so a request
// already holding one keeps a valid response.
//
// Nothing runs in the background. Expiry is checked on lookup and eviction
// happens on insert, which means there is no goroutine to start, stop or leak,
// and the memory bound holds regardless of whether anything is expiring.
package cache

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Key identifies one cached response.
//
// The host header is part of the key because one proxy host can answer for
// several domains and, with PreserveHost on, the upstream sees which one —
// so it may legitimately return different bodies for the same path.
//
// Accepted content codings are part of the key rather than a stored variant
// because the upstream chooses the coding from the request's Accept-Encoding,
// and serving a gzip body to a client that did not offer gzip produces
// garbage. Keying on a four-bit summary rather than the raw header stops the
// cache fragmenting across the dozen spellings browsers actually send.
type Key struct {
	Host  string
	Path  string
	Query string
	Enc   Encodings
}

// Entry is one stored response. Every field is read-only once NewEntry
// returns; see the package comment for why that matters.
type Entry struct {
	Status int
	// Header is already stripped of hop-by-hop fields and Set-Cookie.
	Header http.Header
	Body   []byte

	// ETag and LastModified are lifted out of Header because answering a
	// conditional request touches them on every hit and a map lookup per
	// header per request is not free.
	ETag         string
	LastModified string

	// Public records that the origin marked the response explicitly
	// cacheable by a shared cache. Only such an entry may answer a request
	// that carries credentials.
	Public bool

	StoredAt  time.Time
	ExpiresAt time.Time

	// size is the accounted cost of holding this entry, fixed at
	// construction so eviction never has to walk a header map.
	size int64
}

// entryOverhead approximates the map entry, list node and Go headers that come
// with an entry beyond its body and header bytes. It is a constant rather than
// a measurement because the point is to stop a cache of ten thousand tiny
// objects reporting itself as nearly empty, not to be exact.
const entryOverhead = 256

// NewEntry builds a stored response. The header is copied and sanitised, so
// the caller may keep using the one it passed in.
func NewEntry(status int, header http.Header, body []byte, s Storability, now time.Time) *Entry {
	stored := CopyStorableHeader(header)
	e := &Entry{
		Status:       status,
		Header:       stored,
		Body:         body,
		ETag:         stored.Get("Etag"),
		LastModified: stored.Get("Last-Modified"),
		Public:       s.Public,
		StoredAt:     now,
		ExpiresAt:    s.ExpiresAt,
	}

	size := int64(len(body)) + entryOverhead
	for k, vs := range stored {
		for _, v := range vs {
			size += int64(len(k) + len(v) + 4)
		}
	}
	e.size = size
	return e
}

// Size is what this entry costs against a host's budget.
func (e *Entry) Size() int64 { return e.size }

// Fresh reports whether the entry may still be served without revalidation.
func (e *Entry) Fresh(now time.Time) bool { return now.Before(e.ExpiresAt) }

// Age is how long the entry has been held, for the Age response header. It is
// never negative, since a clock that moved backwards must not produce a header
// no client can parse.
func (e *Entry) Age(now time.Time) time.Duration {
	if age := now.Sub(e.StoredAt); age > 0 {
		return age
	}
	return 0
}

// Stats is what one host's cache has done. It is a snapshot, not a live view.
type Stats struct {
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Stores    uint64 `json:"stores"`
	Evictions uint64 `json:"evictions"`
	// Expired counts entries found stale on lookup, which is a different
	// signal from an eviction: it says the TTL is short, not that the
	// budget is small.
	Expired uint64 `json:"expired"`
	Objects int    `json:"objects"`
	Bytes   int64  `json:"bytes"`
}

// node is an intrusive list element. The key, the entry and both links live in
// one allocation because insertion happens on the miss path, which is already
// the expensive one, and because container/list would add a second allocation
// and an interface round-trip per stored object for no benefit.
type node struct {
	prev, next *node
	key        Key
	entry      *Entry
}

// HostCache is one host's bounded cache.
//
// Eviction is least-recently-used. A static asset cache has a heavily skewed
// access pattern — a handful of bundles take almost all the traffic — which is
// exactly the shape LRU is good at, and it costs two pointer writes per hit
// under a lock the hit path has to take anyway. The alternatives were weighed
// and rejected: least-frequently-used needs a counter per object and a decay
// policy to stop yesterday's favourite pinning itself forever, and evicting by
// expiry alone does not bound memory at all, which is the one thing this has
// to do.
type HostCache struct {
	// budget is atomic because a configuration reload changes it and the
	// request path should not take a lock to read a number it is about to
	// take a lock for anyway.
	budget atomic.Int64

	mu    sync.Mutex
	items map[Key]*node
	// head is the most recently used, tail the first to be evicted.
	head, tail *node
	bytes      int64

	hits, misses, stores, evictions, expired uint64
}

func newHostCache(budget int64) *HostCache {
	h := &HostCache{items: make(map[Key]*node)}
	h.budget.Store(budget)
	return h
}

// Lookup returns a fresh entry for the key, or nil.
//
// A stale entry found here is dropped rather than left for the eviction path:
// it can never be served again, so holding its bytes only makes the cache
// smaller than the operator asked for.
func (h *HostCache) Lookup(k Key, now time.Time) *Entry {
	h.mu.Lock()
	n, ok := h.items[k]
	if !ok {
		h.misses++
		h.mu.Unlock()
		return nil
	}
	if !n.entry.Fresh(now) {
		h.remove(n)
		h.expired++
		h.misses++
		h.mu.Unlock()
		return nil
	}
	h.moveToFront(n)
	h.hits++
	entry := n.entry
	h.mu.Unlock()
	return entry
}

// Put stores an entry, evicting least-recently-used entries until the host is
// back inside its budget.
//
// An entry larger than the whole budget is refused outright rather than stored
// and immediately evicted, which would throw away the entire cache to make
// room for one object that cannot stay.
func (h *HostCache) Put(k Key, e *Entry) {
	budget := h.budget.Load()
	if e.size > budget {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// A concurrent miss on the same key is normal — several requests can be
	// in flight before the first response arrives — and the last writer
	// wins. Replacing rather than keeping the older copy is deliberate: the
	// newer response is the one just observed from the origin.
	if old, ok := h.items[k]; ok {
		h.remove(old)
	}

	n := &node{key: k, entry: e}
	h.items[k] = n
	h.pushFront(n)
	h.bytes += e.size
	h.stores++

	for h.bytes > budget && h.tail != nil {
		h.remove(h.tail)
		h.evictions++
	}
}

// Invalidate drops every variant of a path, whatever content codings were
// stored for it.
func (h *HostCache) Invalidate(host, path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, n := range h.items {
		if k.Host == host && k.Path == path {
			h.remove(n)
		}
	}
}

// Stats snapshots the counters.
func (h *HostCache) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{
		Hits:      h.hits,
		Misses:    h.misses,
		Stores:    h.stores,
		Evictions: h.evictions,
		Expired:   h.expired,
		Objects:   len(h.items),
		Bytes:     h.bytes,
	}
}

// remove unlinks a node and forgets its bytes. The caller holds h.mu.
func (h *HostCache) remove(n *node) {
	delete(h.items, n.key)
	h.bytes -= n.entry.size
	h.unlink(n)
}

func (h *HostCache) unlink(n *node) {
	if n.prev != nil {
		n.prev.next = n.next
	} else {
		h.head = n.next
	}
	if n.next != nil {
		n.next.prev = n.prev
	} else {
		h.tail = n.prev
	}
	n.prev, n.next = nil, nil
}

func (h *HostCache) pushFront(n *node) {
	n.prev = nil
	n.next = h.head
	if h.head != nil {
		h.head.prev = n
	}
	h.head = n
	if h.tail == nil {
		h.tail = n
	}
}

func (h *HostCache) moveToFront(n *node) {
	if h.head == n {
		return
	}
	h.unlink(n)
	h.pushFront(n)
}

// Store holds one HostCache per host.
//
// Hosts are kept apart so one busy host cannot evict another's objects, and so
// a budget means what an operator reading it expects. It also keeps lock
// contention proportional to a single host's traffic rather than to the whole
// proxy's.
type Store struct {
	mu    sync.RWMutex
	hosts map[int64]*HostCache
}

// NewStore builds an empty store.
func NewStore() *Store { return &Store{hosts: make(map[int64]*HostCache)} }

// Host returns the cache for a host, creating it on first use and applying the
// current budget. A lowered budget takes effect on the next insert, which is
// the first moment memory can be released without discarding entries a
// request may already be holding.
func (s *Store) Host(hostID int64, budget int64) *HostCache {
	s.mu.RLock()
	h, ok := s.hosts[hostID]
	s.mu.RUnlock()
	if ok {
		if h.budget.Load() != budget {
			h.budget.Store(budget)
		}
		return h
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Checked again under the write lock: two first requests for the same
	// host race here, and the loser must not replace the winner's cache.
	if h, ok := s.hosts[hostID]; ok {
		h.budget.Store(budget)
		return h
	}
	h = newHostCache(budget)
	s.hosts[hostID] = h
	return h
}

// Forget drops the caches of every host not in keep.
//
// Without this, a host whose cache is switched off — or that is deleted
// outright — keeps its objects in memory until the process restarts, which is
// the one way a bounded cache can still surprise an operator.
func (s *Store) Forget(keep map[int64]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.hosts {
		if _, ok := keep[id]; !ok {
			delete(s.hosts, id)
		}
	}
}

// Stats reports one host's counters, and whether that host has a cache at all.
func (s *Store) Stats(hostID int64) (Stats, bool) {
	s.mu.RLock()
	h, ok := s.hosts[hostID]
	s.mu.RUnlock()
	if !ok {
		return Stats{}, false
	}
	return h.Stats(), true
}
