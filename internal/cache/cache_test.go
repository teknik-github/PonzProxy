package cache

import (
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// bodySize is the payload used by the sizing tests. Every entry then costs
// exactly bodySize+entryOverhead, which makes an eviction boundary something
// the test can state rather than approximate.
const bodySize = 1024

func entry(now time.Time, ttl time.Duration, body []byte) *Entry {
	return NewEntry(http.StatusOK, http.Header{}, body,
		Storability{ExpiresAt: now.Add(ttl), Length: int64(len(body))}, now)
}

func payload(n int) []byte { return make([]byte, n) }

func key(path string) Key { return Key{Host: "example.com", Path: path} }

func TestLookupHitAndMiss(t *testing.T) {
	now := time.Now()
	h := newHostCache(1 << 20)

	if got := h.Lookup(key("/app.js"), now); got != nil {
		t.Fatal("an empty cache returned an entry")
	}

	want := entry(now, time.Minute, []byte("body"))
	h.Put(key("/app.js"), want)

	got := h.Lookup(key("/app.js"), now)
	if got != want {
		t.Fatal("the stored entry was not returned")
	}
	if h.Lookup(key("/other.js"), now) != nil {
		t.Fatal("a different path hit")
	}

	s := h.Stats()
	if s.Hits != 1 || s.Misses != 2 || s.Stores != 1 || s.Objects != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

// TestKeyComponentsAreDistinct guards the parts of the key that would silently
// serve one client another's response if they were dropped.
func TestKeyComponentsAreDistinct(t *testing.T) {
	now := time.Now()
	h := newHostCache(1 << 20)

	base := Key{Host: "a.example.com", Path: "/app.js", Query: "v=1", Enc: EncGzip}
	h.Put(base, entry(now, time.Minute, []byte("first")))

	differs := []struct {
		name string
		key  Key
	}{
		{"another domain on the same host", Key{Host: "b.example.com", Path: "/app.js", Query: "v=1", Enc: EncGzip}},
		{"another path", Key{Host: "a.example.com", Path: "/other.js", Query: "v=1", Enc: EncGzip}},
		{"another query", Key{Host: "a.example.com", Path: "/app.js", Query: "v=2", Enc: EncGzip}},
		{"a client that cannot read gzip", Key{Host: "a.example.com", Path: "/app.js", Query: "v=1"}},
		{"a client that also takes brotli", Key{Host: "a.example.com", Path: "/app.js", Query: "v=1", Enc: EncGzip | EncBrotli}},
	}
	for _, tc := range differs {
		t.Run(tc.name, func(t *testing.T) {
			if h.Lookup(tc.key, now) != nil {
				t.Fatal("hit on a key that differs")
			}
		})
	}
	if h.Lookup(base, now) == nil {
		t.Fatal("the original key stopped hitting")
	}
}

func TestExpiredEntriesAreDroppedOnLookup(t *testing.T) {
	now := time.Now()
	h := newHostCache(1 << 20)
	h.Put(key("/app.js"), entry(now, time.Minute, payload(bodySize)))

	if h.Lookup(key("/app.js"), now.Add(59*time.Second)) == nil {
		t.Fatal("a fresh entry did not hit")
	}
	if h.Lookup(key("/app.js"), now.Add(time.Minute)) != nil {
		t.Fatal("an entry served at the instant it expired")
	}

	s := h.Stats()
	if s.Objects != 0 || s.Bytes != 0 {
		t.Fatalf("the stale entry kept its bytes: %+v", s)
	}
	if s.Expired != 1 {
		t.Fatalf("expiry was not counted separately from eviction: %+v", s)
	}
}

// TestEvictsLeastRecentlyUsed is the behaviour the whole bound rests on: under
// pressure the cache gives up what nobody is asking for.
func TestEvictsLeastRecentlyUsed(t *testing.T) {
	now := time.Now()
	const objects = 4
	h := newHostCache(objects * (bodySize + entryOverhead))

	for i := range objects {
		h.Put(key("/a"+strconv.Itoa(i)+".js"), entry(now, time.Hour, payload(bodySize)))
	}
	if s := h.Stats(); s.Objects != objects || s.Evictions != 0 {
		t.Fatalf("filling to exactly the budget already evicted: %+v", s)
	}

	// Touch everything except /a0.js, making it the least recently used.
	for i := 1; i < objects; i++ {
		if h.Lookup(key("/a"+strconv.Itoa(i)+".js"), now) == nil {
			t.Fatalf("/a%d.js was missing before eviction", i)
		}
	}

	h.Put(key("/new.js"), entry(now, time.Hour, payload(bodySize)))

	if h.Lookup(key("/a0.js"), now) != nil {
		t.Error("the least recently used entry survived")
	}
	for i := 1; i < objects; i++ {
		if h.Lookup(key("/a"+strconv.Itoa(i)+".js"), now) == nil {
			t.Errorf("/a%d.js was evicted although it had just been used", i)
		}
	}
	if h.Lookup(key("/new.js"), now) == nil {
		t.Error("the newest entry was evicted")
	}

	s := h.Stats()
	if s.Evictions != 1 {
		t.Errorf("evictions = %d, want 1", s.Evictions)
	}
	if s.Bytes > h.budget.Load() {
		t.Errorf("bytes = %d over a budget of %d", s.Bytes, h.budget.Load())
	}
}

// TestBudgetHoldsUnderPressure is the property that matters more than which
// entry goes: however many distinct URLs arrive, the memory does not grow.
func TestBudgetHoldsUnderPressure(t *testing.T) {
	now := time.Now()
	const budget = 16 * (bodySize + entryOverhead)
	h := newHostCache(budget)

	for i := range 2000 {
		h.Put(key("/asset"+strconv.Itoa(i)+".js"), entry(now, time.Hour, payload(bodySize)))
		if s := h.Stats(); s.Bytes > budget {
			t.Fatalf("after %d inserts bytes = %d, over the budget of %d", i, s.Bytes, budget)
		}
	}
	if s := h.Stats(); s.Objects != 16 {
		t.Fatalf("objects = %d, want the 16 the budget allows", s.Objects)
	}
}

func TestObjectLargerThanTheBudgetIsRefused(t *testing.T) {
	now := time.Now()
	const budget = 4 * (bodySize + entryOverhead)
	h := newHostCache(budget)

	h.Put(key("/small.js"), entry(now, time.Hour, payload(bodySize)))
	h.Put(key("/huge.bin"), entry(now, time.Hour, payload(budget)))

	if h.Lookup(key("/huge.bin"), now) != nil {
		t.Error("an object that cannot fit was stored anyway")
	}
	if h.Lookup(key("/small.js"), now) == nil {
		t.Error("the whole cache was thrown away to make room for it")
	}
}

func TestReplacingAKeyDoesNotDoubleCount(t *testing.T) {
	now := time.Now()
	h := newHostCache(1 << 20)

	h.Put(key("/app.js"), entry(now, time.Hour, payload(bodySize)))
	first := h.Stats().Bytes

	h.Put(key("/app.js"), entry(now, time.Hour, payload(bodySize)))
	s := h.Stats()
	if s.Bytes != first {
		t.Fatalf("bytes = %d after replacing one entry, want %d", s.Bytes, first)
	}
	if s.Objects != 1 {
		t.Fatalf("objects = %d, want 1", s.Objects)
	}
}

func TestLoweredBudgetTakesEffectOnTheNextInsert(t *testing.T) {
	now := time.Now()
	s := NewStore()
	const budget = 8 * (bodySize + entryOverhead)

	h := s.Host(1, budget)
	for i := range 8 {
		h.Put(key("/a"+strconv.Itoa(i)+".js"), entry(now, time.Hour, payload(bodySize)))
	}

	// A reload halves the budget.
	h = s.Host(1, budget/2)
	h.Put(key("/new.js"), entry(now, time.Hour, payload(bodySize)))

	if got := h.Stats().Bytes; got > budget/2 {
		t.Fatalf("bytes = %d, over the lowered budget of %d", got, budget/2)
	}
}

func TestInvalidateDropsEveryVariant(t *testing.T) {
	now := time.Now()
	h := newHostCache(1 << 20)

	for _, enc := range []Encodings{0, EncGzip, EncGzip | EncBrotli} {
		h.Put(Key{Host: "example.com", Path: "/app.js", Enc: enc},
			entry(now, time.Hour, payload(bodySize)))
	}
	h.Put(Key{Host: "example.com", Path: "/other.js"}, entry(now, time.Hour, payload(bodySize)))

	h.Invalidate("example.com", "/app.js")

	for _, enc := range []Encodings{0, EncGzip, EncGzip | EncBrotli} {
		if h.Lookup(Key{Host: "example.com", Path: "/app.js", Enc: enc}, now) != nil {
			t.Errorf("the %b variant survived invalidation", enc)
		}
	}
	if h.Lookup(Key{Host: "example.com", Path: "/other.js"}, now) == nil {
		t.Error("an unrelated path was invalidated")
	}
	if got := h.Stats().Bytes; got != bodySize+entryOverhead {
		t.Errorf("bytes = %d after invalidation, want one entry's worth", got)
	}
}

func TestStoreForgetsHostsThatAreGone(t *testing.T) {
	now := time.Now()
	s := NewStore()

	for _, id := range []int64{1, 2, 3} {
		s.Host(id, 1<<20).Put(key("/app.js"), entry(now, time.Hour, payload(bodySize)))
	}
	s.Forget(map[int64]struct{}{2: {}})

	if _, ok := s.Stats(1); ok {
		t.Error("host 1 kept its cache after being dropped")
	}
	if _, ok := s.Stats(3); ok {
		t.Error("host 3 kept its cache after being dropped")
	}
	st, ok := s.Stats(2)
	if !ok || st.Objects != 1 {
		t.Errorf("host 2 lost its cache: %+v ok=%v", st, ok)
	}
}

// TestHostsAreIsolated guards the reason budgets are per host: one host filling
// its cache must not cost another one anything.
func TestHostsAreIsolated(t *testing.T) {
	now := time.Now()
	s := NewStore()
	const budget = 2 * (bodySize + entryOverhead)

	s.Host(1, budget).Put(key("/shared-name.js"), entry(now, time.Hour, []byte("host one")))
	for i := range 50 {
		s.Host(2, budget).Put(key("/a"+strconv.Itoa(i)+".js"), entry(now, time.Hour, payload(bodySize)))
	}

	got := s.Host(1, budget).Lookup(key("/shared-name.js"), now)
	if got == nil || string(got.Body) != "host one" {
		t.Fatal("a busy host evicted another host's entry")
	}
}

// TestEntrySizeCountsHeaders stops a cache of header-heavy objects reporting
// itself as nearly empty while holding real memory.
func TestEntrySizeCountsHeaders(t *testing.T) {
	now := time.Now()
	h := http.Header{}
	h.Set("Content-Type", "application/javascript")
	h.Set("Etag", `"abc123"`)

	bare := NewEntry(200, http.Header{}, payload(100), Storability{ExpiresAt: now}, now)
	full := NewEntry(200, h, payload(100), Storability{ExpiresAt: now}, now)

	if full.Size() <= bare.Size() {
		t.Fatalf("headers cost nothing: %d vs %d", full.Size(), bare.Size())
	}
	if bare.Size() != 100+entryOverhead {
		t.Fatalf("size = %d, want body plus the fixed overhead", bare.Size())
	}
}

func TestEntryAgeIsNeverNegative(t *testing.T) {
	now := time.Now()
	e := entry(now, time.Hour, nil)
	if got := e.Age(now.Add(-time.Minute)); got != 0 {
		t.Fatalf("age = %v for a clock that moved backwards", got)
	}
}

// TestConcurrentAccess is meaningful only under -race, which the project's test
// command always passes. It mixes the three things that touch the lock —
// lookups, inserts and eviction pressure — across hosts.
func TestConcurrentAccess(t *testing.T) {
	now := time.Now()
	s := NewStore()
	const (
		workers = 16
		rounds  = 400
		budget  = 8 * (bodySize + entryOverhead)
	)

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hostID := int64(w % 3)
			h := s.Host(hostID, budget)
			for i := range rounds {
				k := key("/a" + strconv.Itoa(i%32) + ".js")
				if e := h.Lookup(k, now); e != nil {
					// Reading the body of an entry another
					// goroutine may be evicting is the race
					// this is here to catch.
					_ = len(e.Body)
				} else {
					h.Put(k, entry(now, time.Hour, payload(bodySize)))
				}
				if i%50 == 0 {
					_ = h.Stats()
				}
			}
		}()
	}
	wg.Wait()

	for id := range int64(3) {
		st, ok := s.Stats(id)
		if !ok {
			t.Fatalf("host %d has no cache", id)
		}
		if st.Bytes > budget {
			t.Fatalf("host %d holds %d bytes, over its budget of %d", id, st.Bytes, budget)
		}
	}
}

// TestForgetDuringAccess covers the reload path racing live traffic.
func TestForgetDuringAccess(t *testing.T) {
	now := time.Now()
	s := NewStore()
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			h := s.Host(int64(i%4), 1<<16)
			h.Put(key("/a.js"), entry(now, time.Hour, payload(256)))
			h.Lookup(key("/a.js"), now)
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			s.Forget(map[int64]struct{}{1: {}, 2: {}})
		}
		close(done)
	}()
	wg.Wait()
}

/* ------------------------------------------------------------ benchmarks -- */

func benchEntry() (*HostCache, Key, *Entry) {
	now := time.Now()
	h := newHostCache(64 << 20)
	k := Key{Host: "example.com", Path: "/assets/app.4f2a9c.js", Query: "", Enc: EncGzip | EncBrotli}

	header := http.Header{}
	header.Set("Content-Type", "application/javascript")
	header.Set("Etag", `"4f2a9c"`)
	header.Set("Cache-Control", "public, max-age=31536000")
	e := NewEntry(200, header, payload(64<<10),
		Storability{ExpiresAt: now.Add(time.Hour), Public: true, Length: 64 << 10}, now)
	h.Put(k, e)
	return h, k, e
}

func BenchmarkLookupHit(b *testing.B) {
	h, k, _ := benchEntry()
	now := time.Now()
	b.ReportAllocs()
	for b.Loop() {
		if h.Lookup(k, now) == nil {
			b.Fatal("miss")
		}
	}
}

func BenchmarkLookupMiss(b *testing.B) {
	h, _, _ := benchEntry()
	miss := Key{Host: "example.com", Path: "/assets/absent.js"}
	now := time.Now()
	b.ReportAllocs()
	for b.Loop() {
		if h.Lookup(miss, now) != nil {
			b.Fatal("hit")
		}
	}
}

// BenchmarkLookupHitParallel is the shape that actually matters: every hit
// takes the host's lock, so the question is what that costs under contention.
func BenchmarkLookupHitParallel(b *testing.B) {
	h, k, _ := benchEntry()
	now := time.Now()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if h.Lookup(k, now) == nil {
				b.Fatal("miss")
			}
		}
	})
}

// BenchmarkPutWithEviction is the worst insert: the budget is full, so every
// store also evicts.
func BenchmarkPutWithEviction(b *testing.B) {
	now := time.Now()
	h := newHostCache(8 * (bodySize + entryOverhead))
	body := payload(bodySize)
	keys := make([]Key, 64)
	for i := range keys {
		keys[i] = key("/a" + strconv.Itoa(i) + ".js")
	}

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		h.Put(keys[i%len(keys)], entry(now, time.Hour, body))
		i++
	}
}

func BenchmarkNewEntry(b *testing.B) {
	now := time.Now()
	header := http.Header{}
	header.Set("Content-Type", "application/javascript")
	header.Set("Etag", `"4f2a9c"`)
	header.Set("Cache-Control", "public, max-age=31536000")
	body := payload(64 << 10)
	terms := Storability{ExpiresAt: now.Add(time.Hour), Length: int64(len(body))}

	b.ReportAllocs()
	for b.Loop() {
		_ = NewEntry(200, header, body, terms, now)
	}
}
