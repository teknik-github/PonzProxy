package proxy

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/cache"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// caches holds every host's cached objects for the life of the process.
//
// There is one data plane per process, so one store; hosts are kept apart
// inside it by id, which is what a per-host budget has to be measured against.
// Nothing is constructed here that the composition root would otherwise own:
// the store has no configuration, no I/O and no lifecycle, and the per-host
// limits travel with each request from the routing table.
// The store lives on the Engine rather than at package scope: two engines in
// one process must not share cached objects, and a package-level var makes
// that impossible to arrange. See Engine.caches.

// CacheStats reports what one host's cache has done, for the console. The
// second result is false for a host that has never cached anything.
func (e *Engine) CacheStats(hostID int64) (cache.Stats, bool) { return e.caches.Stats(hostID) }

// interceptCache is the cache's one entry point on the request path.
//
// It returns nil when the request has been answered in full from memory. When
// it returns a writer, the request must be forwarded upstream through that
// writer, which may be a wrapper that copies a cacheable response into the
// cache as it streams to the client.
//
// It belongs immediately before the request is forwarded, after access control
// and request inspection: a request that would have been refused must not be
// answered from the cache instead, or turning caching on would quietly undo
// both.
//
// The setting is taken as an argument rather than read back off the route, so
// the whole feature can be exercised against a setting and a route without
// standing up a host.
func (e *Engine) interceptCache(w http.ResponseWriter, r *http.Request, rt *route,
	cfg *domain.Cache, start time.Time) http.ResponseWriter {

	// An unsafe method aimed at a cached path invalidates it; see
	// cache.Invalidates. Nothing else about such a request concerns the
	// cache, so it goes upstream untouched.
	if cache.Invalidates(cfg, r) {
		e.caches.Host(rt.host.ID, cfg.MaxBytes).
			Invalidate(normalizeHostHeader(r.Host), r.URL.Path)
		return w
	}
	if !cache.Eligible(cfg, r) {
		return w
	}

	now := time.Now()
	hc := e.caches.Host(rt.host.ID, cfg.MaxBytes)
	key := cacheKey(r)

	if entry := hc.Lookup(key, now); entry != nil {
		// A stored entry that this particular client may not be shown —
		// a credentialed request against a response the origin never
		// marked public — falls through to the upstream. It still counts
		// as a hit on the object, which is what it is.
		if cache.Servable(entry, r) {
			e.serveCached(w, r, rt, start, entry, now)
			return nil
		}
	}

	return &captureWriter{
		ResponseWriter: w,
		req:            r,
		cfg:            cfg,
		host:           hc,
		key:            key,
	}
}

// ForgetCaches releases the cache of every host whose id is not in keep.
//
// Without it a deleted host — or one whose operator has just switched caching
// off — keeps its objects resident until the process restarts, which is the
// one way a cache with a budget can still surprise someone reading it.
func (e *Engine) forgetCaches(keep map[int64]struct{}) { e.caches.Forget(keep) }

// cacheHit and cacheMiss are shared because the value never varies and nothing
// downstream writes to a header slice it did not create.
var (
	cacheHit  = []string{"HIT"}
	cacheMiss = []string{"MISS"}
)

// cacheKey derives the key for a request. Every component is read straight off
// the request with no concatenation, so a lookup costs no allocation at all —
// which is the whole reason a cache hit is worth having.
func cacheKey(r *http.Request) cache.Key {
	return cache.Key{
		Host:  normalizeHostHeader(r.Host),
		Path:  r.URL.Path,
		Query: r.URL.RawQuery,
		Enc:   cache.ParseAcceptEncoding(r.Header.Get("Accept-Encoding")),
	}
}

// serveCached answers from memory, including the 304 a conditional request
// deserves. It accounts for the request itself because it never reaches
// forward, which is what normally does the recording.
func (e *Engine) serveCached(w http.ResponseWriter, r *http.Request, rt *route,
	start time.Time, entry *cache.Entry, now time.Time) {

	h := w.Header()
	// Assigned rather than copied: the stored slices are never written to,
	// and cloning a header map on every hit would put an allocation per
	// header back on the path this feature exists to make cheap.
	for k, vs := range entry.Header {
		h[k] = vs
	}
	// Written straight into the map: every key here is already canonical,
	// and net/http's Set re-derives that on each call, which the profile
	// showed costing more than the lookup it follows.
	h["Age"] = []string{strconv.FormatInt(int64(entry.Age(now).Seconds()), 10)}

	// HSTS is normally applied while forwarding, which a hit skips. A
	// response served from cache must not be the one request that arrives
	// without the header the operator asked for.
	if r.TLS != nil && rt.host.HSTSMaxAge > 0 && h.Get("Strict-Transport-Security") == "" {
		h.Set("Strict-Transport-Security", "max-age="+strconv.Itoa(rt.host.HSTSMaxAge))
	}

	if cache.NotModified(entry, r) {
		// net/http drops Content-Length and Content-Type for a 304 on its
		// own, so the validators and cache directives are all that remain,
		// which is exactly what a revalidating client needs.
		h["X-Cache"] = cacheHit
		w.WriteHeader(http.StatusNotModified)
		e.recordCached(r, rt, start, http.StatusNotModified, 0)
		return
	}

	h["X-Cache"] = cacheHit
	h["Content-Length"] = []string{strconv.Itoa(len(entry.Body))}
	w.WriteHeader(entry.Status)
	// net/http discards the body of a HEAD response, so no special case is
	// needed to stop one being sent.
	n, _ := w.Write(entry.Body)
	e.recordCached(r, rt, start, entry.Status, int64(n))
}

func (e *Engine) recordCached(r *http.Request, rt *route, start time.Time, status int, bytesOut int64) {
	e.record(r, rt, start, status, bytesOut, false)
	e.logAccess(r, rt, nil, start, status, bytesOut, nil)
	e.recordAccess(r, rt, nil, start, status, bytesOut, nil)
}

// captureWriter copies a cacheable response into the cache while it streams to
// the client.
//
// The proxy streams, and it keeps streaming: every byte is handed to the
// client before it is buffered, so a cache miss costs the client nothing in
// latency and a response that turns out to be uncacheable — the wrong status,
// a Set-Cookie, no Content-Length, or simply too large — drops its buffer and
// carries on as a plain pass-through. Nothing is ever held back waiting to
// find out whether it can be cached.
//
// The entry is committed the moment the buffered body reaches the origin's
// Content-Length. That is the only completion signal a response writer gets,
// and it is why Storable refuses responses without one: a body that stops
// short of its declared length simply never commits, so a truncated asset can
// never be stored.
// initialCaptureBuffer is how much is reserved before any of the body has
// arrived. 64 KiB covers most assets outright while keeping the cost of an
// upstream that lies about its Content-Length to something negligible.
const initialCaptureBuffer = 64 << 10

type captureWriter struct {
	http.ResponseWriter

	req  *http.Request
	cfg  *domain.Cache
	host *cache.HostCache
	key  cache.Key

	status int
	terms  cache.Storability
	buf    []byte
	// capturing is false for every response that cannot be stored, which
	// makes Write a plain forward with one branch.
	capturing bool
}

// Unwrap exposes the real writer to http.ResponseController, so flushing,
// hijacking and the deadline setters keep working for streamed responses and
// WebSocket upgrades that happen to pass through here.
func (c *captureWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *captureWriter) WriteHeader(status int) {
	c.status = status
	header := c.ResponseWriter.Header()

	if terms, ok := cache.Storable(c.cfg, c.req, status, header, time.Now()); ok {
		c.terms = terms
		c.capturing = true
		// Deliberately not sized from Content-Length. The header is the
		// upstream's claim, and honouring it up front would let a backend
		// that declares a megabyte and sends nothing cost a megabyte per
		// request in flight — a budget for stored objects is no defence
		// against memory spent on objects that never get stored. The
		// buffer starts small and jumps to the exact declared size the
		// first time real bytes need the room.
		c.buf = make([]byte, 0, min(terms.Length, initialCaptureBuffer))
	}

	// Set for every cache-eligible request, storable or not: an operator
	// looking at why a path never hits needs to see that the proxy
	// considered it at all.
	header["X-Cache"] = cacheMiss
	c.ResponseWriter.WriteHeader(status)

	if c.capturing && c.terms.Length == 0 {
		c.commit()
	}
}

func (c *captureWriter) Write(b []byte) (int, error) {
	// The client first, always. Buffering is bookkeeping the response is
	// not waiting on.
	n, err := c.ResponseWriter.Write(b)
	if !c.capturing || n <= 0 {
		return n, err
	}

	if int64(len(c.buf))+int64(n) > c.terms.Length {
		// The origin sent more than it declared. Its framing cannot be
		// trusted, so neither can the body.
		c.abandon()
		return n, err
	}
	if len(c.buf)+n > cap(c.buf) {
		// Grown to the declared length in one step rather than by
		// doubling: the final size is known, so this costs one copy and
		// leaves the stored entry with no slack to account for.
		grown := make([]byte, len(c.buf), c.terms.Length)
		copy(grown, c.buf)
		c.buf = grown
	}
	c.buf = append(c.buf, b[:n]...)

	if int64(len(c.buf)) == c.terms.Length {
		c.commit()
	}
	return n, err
}

func (c *captureWriter) commit() {
	entry := cache.NewEntry(c.status, c.ResponseWriter.Header(), c.buf, c.terms, time.Now())
	c.host.Put(c.key, entry)
	// The buffer now belongs to the entry, and the response is over; both
	// references are dropped so a keep-alive connection's writer does not
	// pin a body it will never use again.
	c.capturing, c.buf = false, nil
}

func (c *captureWriter) abandon() {
	c.capturing, c.buf = false, nil
}
