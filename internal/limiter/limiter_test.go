package limiter

import (
	"net"
	"sync"
	"testing"
	"time"
)

// newAt builds a limiter whose clock the test drives, so no test sleeps.
func newAt(start time.Time) (*Limiter, func(time.Duration)) {
	l := New()
	var mu sync.Mutex
	now := start
	l.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	return l, func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
}

var ip = net.ParseIP("203.0.113.7")

func TestBurstIsAllowedThenTheRateTakesOver(t *testing.T) {
	l, advance := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{RequestsPerSecond: 10, Burst: 20}

	// A page load is a burst. The whole burst must get through, or the
	// limiter refuses ordinary browsing on the first click.
	for i := range 20 {
		if v, rel := l.Acquire(1, ip, rules); v.Limited {
			t.Fatalf("request %d of the burst was refused: %v", i+1, v.Reason)
		} else {
			rel()
		}
	}

	v, rel := l.Acquire(1, ip, rules)
	rel()
	if !v.Limited || v.Reason != ReasonRate {
		t.Fatalf("the request past the burst should be rate limited, got %+v", v)
	}
	if v.RetryAfter <= 0 {
		t.Error("a rate refusal should say how long to wait")
	}

	// One second later the bucket has refilled by the rate, not by the
	// burst: sustained traffic gets the sustained number.
	advance(time.Second)
	allowed := 0
	for range 20 {
		if v, rel := l.Acquire(1, ip, rules); !v.Limited {
			allowed++
			rel()
		}
	}
	if allowed != 10 {
		t.Errorf("after one second %d requests were allowed, want 10", allowed)
	}
}

func TestARefusedRequestDoesNotDigTheClientDeeper(t *testing.T) {
	l, advance := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{RequestsPerSecond: 1, Burst: 1}

	v, rel := l.Acquire(1, ip, rules)
	rel()
	if v.Limited {
		t.Fatal("the first request should pass")
	}

	// Hammer the limit while it is closed. If refusals consumed tokens, the
	// client would never climb out, and a misbehaving script would lock
	// itself out permanently rather than for a second.
	for range 1000 {
		_, rel := l.Acquire(1, ip, rules)
		rel()
	}

	advance(time.Second)
	if v, rel := l.Acquire(1, ip, rules); v.Limited {
		rel()
		t.Errorf("a second later the client should be allowed again, got %v", v.Reason)
	} else {
		rel()
	}
}

func TestConcurrencyIsBoundedAndReleased(t *testing.T) {
	l, _ := newAt(time.Unix(0, 0))
	defer l.Close()
	// No rate limit, so only the concurrency cap can refuse.
	rules := Rules{MaxConcurrent: 3}

	var held []Release
	for i := range 3 {
		v, rel := l.Acquire(1, ip, rules)
		if v.Limited {
			t.Fatalf("request %d should fit inside the cap", i+1)
		}
		held = append(held, rel)
	}

	v, rel := l.Acquire(1, ip, rules)
	rel()
	if !v.Limited || v.Reason != ReasonConcurrency {
		t.Fatalf("the fourth concurrent request should be refused, got %+v", v)
	}

	held[0]()
	if v, rel := l.Acquire(1, ip, rules); v.Limited {
		rel()
		t.Error("releasing one slot should let the next request in")
	} else {
		rel()
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	l, _ := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{MaxConcurrent: 1}

	_, rel := l.Acquire(1, ip, rules)
	// A handler that both defers a release and calls it on an early return
	// must not free a slot twice, or the cap drifts upwards forever.
	rel()
	rel()
	rel()

	_, rel2 := l.Acquire(1, ip, rules)
	defer rel2()
	v, rel3 := l.Acquire(1, ip, rules)
	rel3()
	if !v.Limited {
		t.Error("the cap should still be 1 after repeated releases")
	}
}

func TestALimitedRequestTakesNoConcurrencySlot(t *testing.T) {
	l, _ := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{RequestsPerSecond: 1, Burst: 1, MaxConcurrent: 10}

	_, rel := l.Acquire(1, ip, rules)
	defer rel()

	// Detect mode forwards a request the limiter judged. It must not also
	// be holding a slot, or a host in detect would slowly exhaust the cap
	// it is not yet enforcing.
	for range 5 {
		v, rel := l.Acquire(1, ip, rules)
		if !v.Limited {
			t.Fatal("expected these to be rate limited")
		}
		rel()
	}

	l.shardsForTest(func(c *client) {
		if c.active != 1 {
			t.Errorf("active = %d, want 1: refused requests took slots", c.active)
		}
	})
}

func TestBudgetsArePerHost(t *testing.T) {
	l, _ := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{RequestsPerSecond: 1, Burst: 1}

	_, rel := l.Acquire(1, ip, rules)
	rel()
	v, rel := l.Acquire(1, ip, rules)
	rel()
	if !v.Limited {
		t.Fatal("host 1 should be limited now")
	}

	// The same visitor on a different host has their own budget: "50 per
	// second for this site" is what an operator means.
	v, rel = l.Acquire(2, ip, rules)
	rel()
	if v.Limited {
		t.Error("a different host should have its own budget")
	}
}

func TestBudgetsArePerAddress(t *testing.T) {
	l, _ := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{RequestsPerSecond: 1, Burst: 1}

	_, rel := l.Acquire(1, net.ParseIP("198.51.100.1"), rules)
	rel()
	v, rel := l.Acquire(1, net.ParseIP("198.51.100.2"), rules)
	rel()
	if v.Limited {
		t.Error("a second address should not inherit the first one's spent budget")
	}
}

func TestAMissingAddressIsNeverRefused(t *testing.T) {
	l, _ := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{RequestsPerSecond: 1, Burst: 1}

	for range 100 {
		v, rel := l.Acquire(1, nil, rules)
		rel()
		if v.Limited {
			t.Fatal("an unparseable client address must not become an outage")
		}
	}
}

func TestIdleRecordsAreSweptButBusyOnesAreNot(t *testing.T) {
	l, advance := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{RequestsPerSecond: 10, Burst: 10, MaxConcurrent: 5}

	_, rel := l.Acquire(1, net.ParseIP("198.51.100.1"), rules)
	rel()
	// Held open: this one is mid-request when the sweep runs.
	_, busy := l.Acquire(1, net.ParseIP("198.51.100.2"), rules)
	defer busy()

	if got := l.Tracked(); got != 2 {
		t.Fatalf("tracked = %d, want 2", got)
	}

	advance(idleAfter + time.Minute)
	l.sweepOnce()

	// Sweeping a record with a request in flight would leave its release
	// decrementing a record that no longer exists.
	if got := l.Tracked(); got != 1 {
		t.Errorf("tracked after the sweep = %d, want 1 (the busy one)", got)
	}
}

func TestForgetDropsOneHostOnly(t *testing.T) {
	l, _ := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{RequestsPerSecond: 5, Burst: 5}

	_, rel := l.Acquire(1, ip, rules)
	rel()
	_, rel = l.Acquire(2, ip, rules)
	rel()

	l.Forget(1)
	if got := l.Tracked(); got != 1 {
		t.Errorf("tracked after forgetting host 1 = %d, want 1", got)
	}
}

func TestTheTableDoesNotGrowWithoutBound(t *testing.T) {
	l, _ := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{RequestsPerSecond: 5, Burst: 5}

	// Far more distinct addresses than the table may hold.
	for i := range 300_000 {
		addr := net.IPv4(byte(i>>24), byte(i>>16), byte(i>>8), byte(i))
		_, rel := l.Acquire(1, addr, rules)
		rel()
	}
	if got, max := l.Tracked(), shards*maxPerShard; got > max {
		t.Errorf("tracked = %d, which is past the %d bound", got, max)
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	l, _ := newAt(time.Unix(0, 0))
	defer l.Close()
	rules := Rules{RequestsPerSecond: 1000, Burst: 1000, MaxConcurrent: 50}

	var wg sync.WaitGroup
	for w := range 16 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			addr := net.IPv4(203, 0, 113, byte(w%4))
			for range 500 {
				_, rel := l.Acquire(int64(w%3), addr, rules)
				rel()
			}
		}(w)
	}
	wg.Wait()
}

func BenchmarkAcquireAllowed(b *testing.B) {
	l := New()
	defer l.Close()
	rules := Rules{RequestsPerSecond: 1_000_000, Burst: 1_000_000, MaxConcurrent: 1_000_000}
	addr := net.ParseIP("203.0.113.9")

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, rel := l.Acquire(1, addr, rules)
		rel()
	}
}
