package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// hostIDs are handed out one per test so that the process-wide cache store
// cannot carry one test's entries into another's.
var nextCacheHostID atomic.Int64

func cacheConfig() *domain.Cache {
	c := domain.DefaultCache()
	c.Enabled = true
	c.Paths = []string{".js", ".css", "/assets/"}
	c.TTL = time.Minute
	c.Normalize()
	return &c
}

// assetBackend serves one asset and counts how often it was actually asked
// for, which is the only measurement that says whether the cache worked.
type assetBackend struct {
	server *httptest.Server
	hits   atomic.Int64
}

func newAssetBackend(t *testing.T, handler http.HandlerFunc) *assetBackend {
	t.Helper()
	b := &assetBackend{}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(b.server.Close)
	return b
}

// cacheRoute wires an engine to one backend and returns the route the request
// path would resolve to.
func cacheRoute(t *testing.T, e *Engine, b *assetBackend) *route {
	t.Helper()
	id := nextCacheHostID.Add(1)
	name := "cache" + strconv.FormatInt(id, 10) + ".example.com"
	h := domain.Host{
		ID: id, Name: name, Enabled: true,
		Domains:     []string{name},
		Algorithm:   domain.RoundRobin,
		HealthCheck: domain.HealthCheck{Enabled: false},
		Upstreams: []domain.Upstream{{
			ID: id, HostID: id, Scheme: "http",
			Address: strings.TrimPrefix(b.server.URL, "http://"),
			Weight:  1, Enabled: true,
		}},
	}
	e.Reload(Config{Hosts: []domain.Host{h}})
	rt := e.table.Load().lookup(name)
	if rt == nil {
		t.Fatal("route was not installed")
	}
	return rt
}

// serve mimics the one line the engine's ServeHTTP adds for the cache, so the
// tests exercise the real request path: hook, then forward through whatever
// writer the hook handed back.
func serve(e *Engine, cfg *domain.Cache, rt *route, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	start := time.Now()

	var w http.ResponseWriter = rec
	if w = e.interceptCache(w, r, rt, cfg, start); w == nil {
		return rec
	}
	e.forward(w, r, rt, start)
	return rec
}

func assetRequest(rt *route, target string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://"+rt.host.Domains[0]+target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}
	return r
}

func plainAsset(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, body)
	}
}

func TestCacheMissThenHit(t *testing.T) {
	const body = "console.log('hello')"
	e := testEngine(t)
	b := newAssetBackend(t, plainAsset(body))
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	first := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
	if first.Body.String() != body {
		t.Fatalf("first body = %q", first.Body.String())
	}
	if got := first.Header().Get("X-Cache"); got != "MISS" {
		t.Fatalf("first X-Cache = %q, want MISS", got)
	}

	second := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
	if second.Body.String() != body {
		t.Fatalf("second body = %q, want the stored copy", second.Body.String())
	}
	if got := second.Header().Get("X-Cache"); got != "HIT" {
		t.Fatalf("second X-Cache = %q, want HIT", got)
	}
	if got := b.hits.Load(); got != 1 {
		t.Fatalf("backend was asked %d times, want 1", got)
	}

	// The response must arrive complete and self-describing, not just with
	// the right bytes.
	if got := second.Header().Get("Content-Type"); got != "application/javascript" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := second.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q, want %d", got, len(body))
	}
	if second.Header().Get("Age") == "" {
		t.Error("a cached response was served without an Age header")
	}
}

func TestCacheKeyedByPathAndQuery(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, func(w http.ResponseWriter, r *http.Request) {
		plainAsset("for "+r.URL.RequestURI())(w, r)
	})
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	targets := []string{"/assets/a.js", "/assets/b.js", "/assets/a.js?v=1", "/assets/a.js?v=2"}
	for _, target := range targets {
		got := serve(e, cfg, rt, assetRequest(rt, target)).Body.String()
		if want := "for " + target; got != want {
			t.Fatalf("body = %q, want %q", got, want)
		}
	}
	if got := b.hits.Load(); got != int64(len(targets)) {
		t.Fatalf("backend was asked %d times, want %d", got, len(targets))
	}
	// Every one of them must now be a hit, and still be its own body.
	for _, target := range targets {
		rec := serve(e, cfg, rt, assetRequest(rt, target))
		if rec.Header().Get("X-Cache") != "HIT" {
			t.Errorf("%s did not hit", target)
		}
		if want := "for " + target; rec.Body.String() != want {
			t.Errorf("%s served %q, want %q", target, rec.Body.String(), want)
		}
	}
	if got := b.hits.Load(); got != int64(len(targets)) {
		t.Fatalf("backend was asked %d times after the second pass", got)
	}
}

// TestCacheKeyedByAcceptEncoding is the variant bug that would otherwise hand a
// client a compressed body it never said it could read.
//
// Brotli is the discriminator rather than gzip because Go's transport adds its
// own "Accept-Encoding: gzip" to a request that has none and transparently
// decompresses the reply, which would hide the very distinction under test.
func TestCacheKeyedByAcceptEncoding(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, func(w http.ResponseWriter, r *http.Request) {
		body := "identity payload"
		w.Header().Set("Vary", "Accept-Encoding")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "br") {
			body = "brotli payload"
			w.Header().Set("Content-Encoding", "br")
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, body)
	})
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	compressed := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", "Accept-Encoding", "br, gzip"))
	if compressed.Body.String() != "brotli payload" {
		t.Fatalf("body = %q", compressed.Body.String())
	}

	plain := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
	if plain.Body.String() != "identity payload" {
		t.Fatalf("a client that cannot read brotli was served %q", plain.Body.String())
	}
	if plain.Header().Get("X-Cache") != "MISS" {
		t.Error("the two variants shared a cache entry")
	}

	// Both variants are now stored, each under its own key.
	again := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", "Accept-Encoding", "br, gzip"))
	if again.Header().Get("X-Cache") != "HIT" || again.Body.String() != "brotli payload" {
		t.Fatalf("brotli variant: X-Cache=%q body=%q", again.Header().Get("X-Cache"), again.Body.String())
	}
	if got := b.hits.Load(); got != 2 {
		t.Fatalf("backend was asked %d times, want one per variant", got)
	}
}

// TestOriginRefusalsWinOverTheHostSetting is the rule that keeps this feature
// safe to switch on: the application knows what is in the body.
func TestOriginRefusalsWinOverTheHostSetting(t *testing.T) {
	cases := []struct {
		name    string
		respond http.HandlerFunc
	}{
		{"no-store", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			plainAsset("x")(w, r)
		}},
		{"no-cache", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			plainAsset("x")(w, r)
		}},
		{"private", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, max-age=600")
			plainAsset("x")(w, r)
		}},
		{"max-age=0", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "max-age=0")
			plainAsset("x")(w, r)
		}},
		{"sets a cookie", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Set-Cookie", "sid=1")
			plainAsset("x")(w, r)
		}},
		{"varies on something unkeyed", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Vary", "Cookie")
			plainAsset("x")(w, r)
		}},
		{"error status", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}},
		{"not found", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "gone", http.StatusNotFound)
		}},
		{"redirect", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "/elsewhere.js")
			w.WriteHeader(http.StatusMovedPermanently)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := testEngine(t)
			b := newAssetBackend(t, tc.respond)
			rt := cacheRoute(t, e, b)
			cfg := cacheConfig()

			serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
			rec := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))

			if rec.Header().Get("X-Cache") == "HIT" {
				t.Error("the response was cached although the origin refused")
			}
			if got := b.hits.Load(); got != 2 {
				t.Errorf("backend was asked %d times, want 2", got)
			}
		})
	}
}

func TestCredentialedRequestsNeedAnExplicitlyPublicResponse(t *testing.T) {
	cases := []struct {
		name         string
		requestHead  []string
		cacheControl string
		wantHits     int64
	}{
		{"authorization, ordinary response", []string{"Authorization", "Bearer x"}, "max-age=600", 2},
		{"authorization, public response", []string{"Authorization", "Bearer x"}, "public, max-age=600", 1},
		{"cookie, ordinary response", []string{"Cookie", "sid=1"}, "max-age=600", 2},
		{"cookie, public response", []string{"Cookie", "sid=1"}, "public, max-age=600", 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := testEngine(t)
			b := newAssetBackend(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", tc.cacheControl)
				plainAsset("secret-ish")(w, r)
			})
			rt := cacheRoute(t, e, b)
			cfg := cacheConfig()

			serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", tc.requestHead...))
			serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", tc.requestHead...))

			if got := b.hits.Load(); got != tc.wantHits {
				t.Fatalf("backend was asked %d times, want %d", got, tc.wantHits)
			}
		})
	}
}

// TestCredentialedRequestCannotReadASharedCopy is the other half of the same
// rule: a response cached for anonymous visitors must not be handed to a
// request carrying a session, in case the origin would have personalised it.
func TestCredentialedRequestCannotReadASharedCopy(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, plainAsset("shared body"))
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
	if serve(e, cfg, rt, assetRequest(rt, "/assets/app.js")).Header().Get("X-Cache") != "HIT" {
		t.Fatal("the anonymous copy was not cached")
	}

	rec := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", "Cookie", "sid=1"))
	if rec.Header().Get("X-Cache") == "HIT" {
		t.Fatal("a credentialed request was served a shared cached copy")
	}
	if got := b.hits.Load(); got != 2 {
		t.Fatalf("backend was asked %d times, want 2", got)
	}
}

func TestClientDirectivesAreHonoured(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, plainAsset("body"))
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))

	// no-cache forbids serving the stored copy but not keeping one.
	forced := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", "Cache-Control", "no-cache"))
	if forced.Header().Get("X-Cache") == "HIT" {
		t.Error("a no-cache request was answered from the cache")
	}
	// no-store keeps the request out of the cache entirely, so the response
	// is not even labelled.
	bypass := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", "Cache-Control", "no-store"))
	if bypass.Header().Get("X-Cache") != "" {
		t.Errorf("a no-store request was handled by the cache: %q", bypass.Header().Get("X-Cache"))
	}
	// A range request is passed through untouched for the same reason.
	ranged := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", "Range", "bytes=0-3"))
	if ranged.Header().Get("X-Cache") != "" {
		t.Errorf("a range request was handled by the cache: %q", ranged.Header().Get("X-Cache"))
	}
}

func TestConditionalRequestGetsA304(t *testing.T) {
	const body = "body with a validator"
	modified := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	e := testEngine(t)
	b := newAssetBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Etag", `"v1"`)
		w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))
		plainAsset(body)(w, r)
	})
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	first := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
	if first.Header().Get("Etag") != `"v1"` {
		t.Fatalf("the validator was not passed through: %v", first.Header())
	}

	matched := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", "If-None-Match", `"v1"`))
	if matched.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", matched.Code)
	}
	if matched.Body.Len() != 0 {
		t.Errorf("a 304 carried %d bytes of body", matched.Body.Len())
	}
	if matched.Header().Get("Etag") != `"v1"` {
		t.Error("the 304 did not repeat the validator")
	}

	weak := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", "If-None-Match", `W/"v1"`))
	if weak.Code != http.StatusNotModified {
		t.Errorf("weak validator: status = %d, want 304", weak.Code)
	}

	dated := serve(e, cfg, rt, assetRequest(rt,
		"/assets/app.js", "If-Modified-Since", modified.Format(http.TimeFormat)))
	if dated.Code != http.StatusNotModified {
		t.Errorf("If-Modified-Since: status = %d, want 304", dated.Code)
	}

	stale := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js", "If-None-Match", `"v0"`))
	if stale.Code != http.StatusOK || stale.Body.String() != body {
		t.Errorf("a non-matching validator should get the whole body, got %d %q",
			stale.Code, stale.Body.String())
	}

	if got := b.hits.Load(); got != 1 {
		t.Fatalf("backend was asked %d times; every revalidation should have been local", got)
	}
}

func TestStaleEntryIsRefetched(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=1")
		plainAsset("body")(w, r)
	})
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
	if serve(e, cfg, rt, assetRequest(rt, "/assets/app.js")).Header().Get("X-Cache") != "HIT" {
		t.Fatal("the response was not cached")
	}

	// Reaching past the origin's own max-age rather than sleeping for it.
	hc := e.caches.Host(rt.host.ID, cfg.MaxBytes)
	if hc.Lookup(cacheKey(assetRequest(rt, "/assets/app.js")), time.Now().Add(2*time.Second)) != nil {
		t.Fatal("the entry outlived the origin's max-age")
	}

	if serve(e, cfg, rt, assetRequest(rt, "/assets/app.js")).Header().Get("X-Cache") != "MISS" {
		t.Fatal("the expired entry was not refetched")
	}
	if got := b.hits.Load(); got != 2 {
		t.Fatalf("backend was asked %d times, want 2", got)
	}
}

// TestChunkedResponsesStreamThrough is the buffering decision made visible: a
// response with no declared length cannot be told apart from a truncated one,
// so it is never stored — and it is never delayed either.
func TestChunkedResponsesStreamThrough(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.WriteHeader(http.StatusOK)
		// Flushing before the handler returns forces chunked framing, so
		// no Content-Length ever reaches the proxy.
		w.(http.Flusher).Flush()
		io.WriteString(w, "first ")
		w.(http.Flusher).Flush()
		io.WriteString(w, "second")
	})
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	first := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
	if first.Body.String() != "first second" {
		t.Fatalf("body = %q; streaming was broken", first.Body.String())
	}
	if first.Header().Get("Content-Length") != "" {
		t.Error("a length was invented for a chunked response")
	}

	second := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
	if second.Header().Get("X-Cache") == "HIT" {
		t.Error("a response with no declared length was stored")
	}
	if second.Body.String() != "first second" {
		t.Fatalf("second body = %q", second.Body.String())
	}
	if got := b.hits.Load(); got != 2 {
		t.Fatalf("backend was asked %d times, want 2", got)
	}
}

// TestOversizedResponsesStreamThroughIntact is the other half of that: the
// per-object limit must never cost the client bytes.
func TestOversizedResponsesStreamThroughIntact(t *testing.T) {
	body := strings.Repeat("payload!", 4096) // 32 KiB

	e := testEngine(t)
	b := newAssetBackend(t, plainAsset(body))
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()
	cfg.MaxObjectBytes = 4096

	for range 2 {
		rec := serve(e, cfg, rt, assetRequest(rt, "/assets/big.js"))
		if rec.Body.String() != body {
			t.Fatalf("body was %d bytes, want %d — the client lost data",
				rec.Body.Len(), len(body))
		}
		if rec.Header().Get("X-Cache") == "HIT" {
			t.Fatal("an oversized response was stored")
		}
	}
	if got := b.hits.Load(); got != 2 {
		t.Fatalf("backend was asked %d times, want 2", got)
	}
}

// TestLargeAssetIsStoredIntact crosses the point where the capture buffer stops
// being the small reservation it starts as and grows to the declared length.
func TestLargeAssetIsStoredIntact(t *testing.T) {
	body := strings.Repeat("abcdefgh", 25*1024) // 200 KiB, past initialCaptureBuffer

	e := testEngine(t)
	b := newAssetBackend(t, plainAsset(body))
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	if got := serve(e, cfg, rt, assetRequest(rt, "/assets/big.js")).Body.String(); got != body {
		t.Fatalf("miss returned %d bytes, want %d", len(got), len(body))
	}

	hit := serve(e, cfg, rt, assetRequest(rt, "/assets/big.js"))
	if hit.Header().Get("X-Cache") != "HIT" {
		t.Fatal("a 200 KiB asset inside the object limit was not stored")
	}
	if got := hit.Body.String(); got != body {
		t.Fatalf("hit returned %d bytes, want %d", len(got), len(body))
	}
	if got := b.hits.Load(); got != 1 {
		t.Fatalf("backend was asked %d times, want 1", got)
	}
}

func TestEvictionUnderPressureKeepsServingCorrectBodies(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, func(w http.ResponseWriter, r *http.Request) {
		plainAsset(strings.Repeat("x", 1024)+r.URL.Path)(w, r)
	})
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()
	cfg.MaxObjectBytes = 4096
	cfg.MaxBytes = 4 * 4096

	for i := range 40 {
		target := "/assets/a" + strconv.Itoa(i) + ".js"
		rec := serve(e, cfg, rt, assetRequest(rt, target))
		want := strings.Repeat("x", 1024) + target
		if rec.Body.String() != want {
			t.Fatalf("%s served the wrong body", target)
		}
	}

	stats, ok := e.CacheStats(rt.host.ID)
	if !ok {
		t.Fatal("no stats for a host that has been caching")
	}
	if stats.Bytes > cfg.MaxBytes {
		t.Fatalf("cache holds %d bytes, over the budget of %d", stats.Bytes, cfg.MaxBytes)
	}
	if stats.Evictions == 0 {
		t.Error("40 objects into a four-object budget evicted nothing")
	}
}

func TestUnsafeMethodInvalidatesThePath(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, plainAsset("body"))
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
	if serve(e, cfg, rt, assetRequest(rt, "/assets/app.js")).Header().Get("X-Cache") != "HIT" {
		t.Fatal("the response was not cached")
	}

	put := httptest.NewRequest(http.MethodPut,
		"http://"+rt.host.Domains[0]+"/assets/app.js", strings.NewReader("new"))
	serve(e, cfg, rt, put)

	if serve(e, cfg, rt, assetRequest(rt, "/assets/app.js")).Header().Get("X-Cache") != "MISS" {
		t.Fatal("the cached copy survived a write to the same path")
	}
}

func TestUnlistedPathsAreUntouched(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, plainAsset("api response"))
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	for range 2 {
		rec := serve(e, cfg, rt, assetRequest(rt, "/api/orders"))
		if rec.Header().Get("X-Cache") != "" {
			t.Fatalf("an unlisted path was handled by the cache: %q", rec.Header().Get("X-Cache"))
		}
	}
	if got := b.hits.Load(); got != 2 {
		t.Fatalf("backend was asked %d times, want 2", got)
	}
}

func TestDisabledCacheAddsNothing(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, plainAsset("body"))
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()
	cfg.Enabled = false

	for range 3 {
		rec := serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
		if rec.Header().Get("X-Cache") != "" {
			t.Fatal("a disabled cache touched the response")
		}
	}
	if got := b.hits.Load(); got != 3 {
		t.Fatalf("backend was asked %d times, want 3", got)
	}
	if _, ok := e.CacheStats(rt.host.ID); ok {
		t.Error("a disabled cache allocated a store for the host")
	}
}

func TestHeadIsServedFromCacheWithoutABody(t *testing.T) {
	const body = "console.log(1)"
	e := testEngine(t)
	b := newAssetBackend(t, plainAsset(body))
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))

	head := httptest.NewRequest(http.MethodHead, "http://"+rt.host.Domains[0]+"/assets/app.js", nil)
	rec := serve(e, cfg, rt, head)
	if rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("HEAD did not hit: %q", rec.Header().Get("X-Cache"))
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q, want %d", got, len(body))
	}
	if got := b.hits.Load(); got != 1 {
		t.Fatalf("backend was asked %d times, want 1", got)
	}
}

// TestForgetCachesReleasesRemovedHosts covers the reload path: a host that
// stops caching must not keep its objects resident.
func TestForgetCachesReleasesRemovedHosts(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, plainAsset("body"))
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()

	serve(e, cfg, rt, assetRequest(rt, "/assets/app.js"))
	if _, ok := e.CacheStats(rt.host.ID); !ok {
		t.Fatal("nothing was cached")
	}

	e.forgetCaches(map[int64]struct{}{})
	if _, ok := e.CacheStats(rt.host.ID); ok {
		t.Fatal("the host kept its cache after being forgotten")
	}
}

// TestConcurrentRequests is meaningful under -race: many clients race on the
// same objects while the cache fills and evicts underneath them.
func TestConcurrentRequests(t *testing.T) {
	e := testEngine(t)
	b := newAssetBackend(t, func(w http.ResponseWriter, r *http.Request) {
		plainAsset(strings.Repeat("y", 512)+r.URL.Path)(w, r)
	})
	rt := cacheRoute(t, e, b)
	cfg := cacheConfig()
	cfg.MaxObjectBytes = 4096
	cfg.MaxBytes = 8 * 4096

	var wg sync.WaitGroup
	for w := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 40 {
				target := "/assets/a" + strconv.Itoa((i+w)%24) + ".js"
				rec := serve(e, cfg, rt, assetRequest(rt, target))
				if rec.Code != http.StatusOK {
					t.Errorf("%s: status %d", target, rec.Code)
					return
				}
				if want := strings.Repeat("y", 512) + target; rec.Body.String() != want {
					t.Errorf("%s served a body belonging to another object", target)
					return
				}
			}
		}()
	}
	wg.Wait()

	stats, _ := e.CacheStats(rt.host.ID)
	if stats.Bytes > cfg.MaxBytes {
		t.Fatalf("cache holds %d bytes, over the budget of %d", stats.Bytes, cfg.MaxBytes)
	}
}

/* ------------------------------------------------------------ benchmarks -- */

// benchWriter is a response writer with no bookkeeping of its own, so the
// numbers below are the cache's cost and nothing else.
type benchWriter struct{ header http.Header }

func (b *benchWriter) Header() http.Header         { return b.header }
func (b *benchWriter) WriteHeader(int)             {}
func (b *benchWriter) Write(p []byte) (int, error) { return len(p), nil }

func benchSetup(b *testing.B, hostID int64) (*Engine, *route, *domain.Cache) {
	b.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(Options{Logger: logger, MaxRetries: 2})
	b.Cleanup(e.Close)

	rt := &route{host: domain.Host{ID: hostID, Name: "bench", Domains: []string{"bench.example.com"}}}
	return e, rt, cacheConfig()
}

func benchRequest(target string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://bench.example.com"+target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}
	return r
}

// primeCache stores one object through the same path a real miss takes. It
// tolerates the entry already being there, because a benchmark body driven by
// RunParallel is entered more than once.
func primeCache(e *Engine, rt *route, cfg *domain.Cache, r *http.Request) {
	if w := e.interceptCache(&benchWriter{header: upstreamHeader()}, r, rt, cfg, time.Now()); w != nil {
		w.WriteHeader(http.StatusOK)
		w.Write(make([]byte, 1024))
	}
}

func upstreamHeader() http.Header {
	h := make(http.Header)
	h.Set("Content-Type", "application/javascript")
	h.Set("Etag", `"4f2a9c"`)
	h.Set("Cache-Control", "public, max-age=31536000")
	h.Set("Content-Length", "1024")
	return h
}

// BenchmarkCacheHit is the path the feature exists for: a request answered
// entirely from memory, with no upstream involved.
func BenchmarkCacheHit(b *testing.B) {
	e, rt, cfg := benchSetup(b, 9001)
	r := benchRequest("/assets/app.4f2a9c.js", "Accept-Encoding", "gzip, deflate, br")
	primeCache(e, rt, cfg, r)

	out := &benchWriter{header: make(http.Header)}
	b.ReportAllocs()
	for b.Loop() {
		if e.interceptCache(out, r, rt, cfg, time.Now()) != nil {
			b.Fatal("expected a hit")
		}
	}
}

// BenchmarkCacheRevalidation is the cheapest useful outcome: a conditional
// request answered with 304 and no body at all.
func BenchmarkCacheRevalidation(b *testing.B) {
	e, rt, cfg := benchSetup(b, 9002)
	primeCache(e, rt, cfg, benchRequest("/assets/app.4f2a9c.js"))

	r := benchRequest("/assets/app.4f2a9c.js", "If-None-Match", `"4f2a9c"`)
	out := &benchWriter{header: make(http.Header)}
	b.ReportAllocs()
	for b.Loop() {
		if e.interceptCache(out, r, rt, cfg, time.Now()) != nil {
			b.Fatal("expected a hit")
		}
	}
}

// BenchmarkCacheMiss walks the whole storing path: a lookup that misses, the
// capture writer, and the buffered body committed to the cache. The budget
// holds far fewer objects than there are paths, so every iteration really is a
// miss followed by an eviction — the worst case, not the average.
func BenchmarkCacheMiss(b *testing.B) {
	e, rt, cfg := benchSetup(b, 9003)
	cfg.MaxBytes = 8 * (1024 + 256)
	// Started from empty so that every iteration really is a miss, however
	// many times the benchmark itself is repeated.
	e.forgetCaches(map[int64]struct{}{})

	requests := make([]*http.Request, 64)
	for i := range requests {
		requests[i] = benchRequest("/assets/a" + strconv.Itoa(i) + ".js")
	}
	body := make([]byte, 1024)

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		out := &benchWriter{header: upstreamHeader()}
		w := e.interceptCache(out, requests[i%len(requests)], rt, cfg, time.Now())
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		i++
	}
}

// BenchmarkCacheUncacheable is the miss that never stores anything: the
// response turns out not to be cacheable, so the capture writer degrades to a
// plain pass-through. It is the cost every non-asset response on a cached path
// pays for the feature being on.
func BenchmarkCacheUncacheable(b *testing.B) {
	e, rt, cfg := benchSetup(b, 9004)
	r := benchRequest("/assets/app.js")
	body := make([]byte, 1024)

	header := upstreamHeader()
	header.Set("Cache-Control", "no-store")

	b.ReportAllocs()
	for b.Loop() {
		out := &benchWriter{header: header}
		w := e.interceptCache(out, r, rt, cfg, time.Now())
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}
}

// BenchmarkCacheDisabled is the number that decides whether every host that
// never turns this on still pays for it.
func BenchmarkCacheDisabled(b *testing.B) {
	e, rt, cfg := benchSetup(b, 9005)
	cfg.Enabled = false
	r := benchRequest("/assets/app.4f2a9c.js")
	out := &benchWriter{header: make(http.Header)}

	b.ReportAllocs()
	for b.Loop() {
		if e.interceptCache(out, r, rt, cfg, time.Now()) == nil {
			b.Fatal("a disabled cache answered the request")
		}
	}
}

// BenchmarkCacheHitParallel is the same hit under contention, which is the
// only version of it a busy proxy ever runs.
func BenchmarkCacheHitParallel(b *testing.B) {
	e, rt, cfg := benchSetup(b, 9006)
	r := benchRequest("/assets/app.4f2a9c.js")
	primeCache(e, rt, cfg, r)

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		out := &benchWriter{header: make(http.Header)}
		for pb.Next() {
			if e.interceptCache(out, r, rt, cfg, time.Now()) != nil {
				b.Fatal("expected a hit")
			}
		}
	})
}

// BenchmarkForwardUpstream is the number every one above is measured against:
// what the same request costs when it has to reach a backend on the loopback
// interface, which is the most favourable upstream that exists.
func BenchmarkForwardUpstream(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(Options{Logger: logger, MaxRetries: 2})
	b.Cleanup(e.Close)

	body := strings.Repeat("x", 1024)
	srv := httptest.NewServer(plainAsset(body))
	b.Cleanup(srv.Close)

	h := domain.Host{
		ID: 9100, Name: "bench", Enabled: true,
		Domains:     []string{"bench.example.com"},
		Algorithm:   domain.RoundRobin,
		HealthCheck: domain.HealthCheck{Enabled: false},
		Upstreams: []domain.Upstream{{
			ID: 1, HostID: 9100, Scheme: "http",
			Address: strings.TrimPrefix(srv.URL, "http://"),
			Weight:  1, Enabled: true,
		}},
	}
	e.Reload(Config{Hosts: []domain.Host{h}})
	rt := e.table.Load().lookup("bench.example.com")

	r := benchRequest("/assets/app.js")
	out := &benchWriter{header: make(http.Header)}

	b.ReportAllocs()
	for b.Loop() {
		e.forward(out, r, rt, time.Now())
	}
}
