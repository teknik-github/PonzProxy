package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Login throttling parameters.
//
// bcrypt already makes each attempt cost roughly half a second of CPU, which
// slows a single attacker but does nothing about many in parallel — and the
// console is reachable from the whole network it is installed on. These bounds
// turn a password guess into a rate an operator would notice long before it
// succeeds, while staying well clear of anything a person typing their own
// password would hit.
const (
	// loginBurst is how many failures one source may make before the
	// window closes on it.
	loginBurst = 5
	// loginWindow is how long failures are remembered.
	loginWindow = 15 * time.Minute
	// loginBlock is how long a source is refused once it exceeds the burst.
	loginBlock = 15 * time.Minute
	// limiterSweep is how often expired entries are discarded, so a long
	// spray of unique addresses cannot grow the map without bound.
	limiterSweep = 5 * time.Minute
)

// loginLimiter throttles failed sign-ins per source address.
//
// Only failures count. A correct password always gets through and clears the
// record, so an operator who mistypes twice and then succeeds is never locked
// out, and a shared NAT address is not held against everyone behind it for
// longer than the one attacker's own window.
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginAttempts
	// now is injectable so the tests do not have to sleep out a window.
	now func() time.Time
}

type loginAttempts struct {
	failures int
	// first marks the start of the current window; reaching loginBurst
	// inside it sets blockedUntil.
	first        time.Time
	blockedUntil time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: make(map[string]*loginAttempts), now: time.Now}
}

// allow reports whether a sign-in attempt from key may proceed, and how long
// the caller must wait when it may not.
func (l *loginLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	entry, found := l.entries[key]
	if !found {
		return true, 0
	}
	if now.Before(entry.blockedUntil) {
		return false, entry.blockedUntil.Sub(now)
	}
	// The block has expired; the next failure starts a fresh window.
	if !entry.blockedUntil.IsZero() {
		delete(l.entries, key)
	}
	return true, 0
}

// recordFailure counts one rejected sign-in.
func (l *loginLimiter) recordFailure(key string) {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	entry, found := l.entries[key]
	if !found || now.Sub(entry.first) > loginWindow {
		l.entries[key] = &loginAttempts{failures: 1, first: now}
		return
	}

	entry.failures++
	if entry.failures >= loginBurst {
		entry.blockedUntil = now.Add(loginBlock)
	}
}

// recordSuccess clears the record for a source that signed in.
func (l *loginLimiter) recordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// sweep drops entries that can no longer refuse anything. Without it, a spray
// of attempts from many addresses would retain one map entry each.
func (l *loginLimiter) sweep() {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	for key, entry := range l.entries {
		expired := now.After(entry.blockedUntil) && now.Sub(entry.first) > loginWindow
		if expired {
			delete(l.entries, key)
		}
	}
}

// run sweeps periodically until ctx is done. It is started by the server.
func (l *loginLimiter) run(stop <-chan struct{}) {
	ticker := time.NewTicker(limiterSweep)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			l.sweep()
		}
	}
}

// size reports how many sources are being tracked. Tests use it to prove the
// sweep actually reclaims entries.
func (l *loginLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// limiterKey identifies the source of a sign-in attempt.
//
// It is the connection's own address, never a forwarded header: a header can
// be set by whoever is guessing, so keying on one would let an attacker reset
// their own budget with every request. Installations behind a reverse proxy
// therefore throttle per upstream proxy, which is the correct conservative
// behaviour when the real client cannot be trusted.
func limiterKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
