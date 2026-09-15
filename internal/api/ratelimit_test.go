package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fixedClock lets the tests cross a 15-minute window without sleeping.
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testLimiter() (*loginLimiter, *fixedClock) {
	clock := &fixedClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	l := newLoginLimiter()
	l.now = clock.Now
	return l, clock
}

func TestLimiterBlocksAfterTheBurst(t *testing.T) {
	l, clock := testLimiter()
	const key = "203.0.113.9"

	for i := range loginBurst - 1 {
		l.recordFailure(key)
		if ok, _ := l.allow(key); !ok {
			t.Fatalf("blocked after %d failures, want at least %d allowed", i+1, loginBurst)
		}
	}

	l.recordFailure(key) // this is the one that crosses the burst
	ok, retryAfter := l.allow(key)
	if ok {
		t.Fatal("still allowed after exceeding the burst")
	}
	if retryAfter <= 0 || retryAfter > loginBlock {
		t.Errorf("retryAfter = %v, want between 0 and %v", retryAfter, loginBlock)
	}

	// Still blocked just before the block expires, allowed just after.
	clock.advance(loginBlock - time.Minute)
	if ok, _ := l.allow(key); ok {
		t.Error("block lifted early")
	}
	clock.advance(2 * time.Minute)
	if ok, _ := l.allow(key); !ok {
		t.Error("block never lifted")
	}
}

func TestLimiterIsPerSource(t *testing.T) {
	l, _ := testLimiter()

	for range loginBurst {
		l.recordFailure("203.0.113.9")
	}
	if ok, _ := l.allow("203.0.113.9"); ok {
		t.Fatal("the offending source was not blocked")
	}
	if ok, _ := l.allow("198.51.100.4"); !ok {
		t.Error("an unrelated source was blocked")
	}
}

func TestSuccessClearsTheRecord(t *testing.T) {
	l, _ := testLimiter()
	const key = "203.0.113.9"

	// An operator who mistypes a few times and then gets it right must not
	// be one mistake away from a lockout.
	for range loginBurst - 1 {
		l.recordFailure(key)
	}
	l.recordSuccess(key)

	for range loginBurst - 1 {
		l.recordFailure(key)
		if ok, _ := l.allow(key); !ok {
			t.Fatal("a success did not reset the failure budget")
		}
	}
}

func TestFailuresExpireWithTheWindow(t *testing.T) {
	l, clock := testLimiter()
	const key = "203.0.113.9"

	for range loginBurst - 1 {
		l.recordFailure(key)
	}
	// Once the window passes, the old failures no longer count towards a
	// block: this is a rate limit, not a permanent tally.
	clock.advance(loginWindow + time.Minute)
	for range loginBurst - 1 {
		l.recordFailure(key)
		if ok, _ := l.allow(key); !ok {
			t.Fatal("failures from an expired window still counted")
		}
	}
}

func TestSweepReclaimsExpiredEntries(t *testing.T) {
	l, clock := testLimiter()

	for i := range 50 {
		l.recordFailure(string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	if l.size() == 0 {
		t.Fatal("no entries were recorded")
	}

	clock.advance(loginWindow + loginBlock + time.Minute)
	l.sweep()
	if n := l.size(); n != 0 {
		t.Errorf("%d entries survived the sweep, want 0", n)
	}
}

func TestLimiterKeyIgnoresForwardedHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	r.RemoteAddr = "203.0.113.9:44321"
	// A client that could reset its own budget by setting a header would
	// make the limit worthless.
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	r.Header.Set("CF-Connecting-IP", "10.0.0.2")

	if got := limiterKey(r); got != "203.0.113.9" {
		t.Errorf("limiterKey = %q, want the peer address", got)
	}
}

func TestLimiterIsConcurrencySafe(t *testing.T) {
	l, _ := testLimiter()

	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			key := string(rune('a' + g%4))
			for range 200 {
				l.allow(key)
				l.recordFailure(key)
				l.recordSuccess(key)
				l.sweep()
			}
		}(g)
	}
	wg.Wait()
}
