package proxy

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"
)

// Basic auth stores passwords with bcrypt, which is deliberately slow: a
// single verification at the configured cost takes on the order of a hundred
// milliseconds. That is correct for a login form, and ruinous on a proxy hot
// path — measured at roughly 400ms per request, against 6ms for the same host
// without an access list.
//
// This cache remembers that a specific credential was accepted, so a client
// repeating the same Authorization header pays the hash once per TTL rather
// than once per request.
const (
	// authTTL bounds how long a password change or a removed user can keep
	// working. Short enough that revocation is prompt, long enough that a
	// browser's request burst costs one hash.
	authTTL = 30 * time.Second
	// authCacheMax caps the entry count. Each distinct credential seen adds
	// one, so an attacker sending varied passwords must not be able to grow
	// it without bound.
	authCacheMax = 4096
)

// authCache stores the fact that a credential was verified. Only grants are
// cached: a refusal stays expensive, which is what keeps the proxy from
// becoming a fast oracle for guessing passwords.
type authCache struct {
	mu      sync.Mutex
	entries map[authKey]time.Time // value is the expiry
}

// authKey identifies a credential without retaining the password. The list's
// UpdatedAt is part of the key, so editing a list — changing a password,
// removing a user — invalidates every entry for it immediately rather than
// waiting out the TTL.
type authKey struct {
	listID      int64
	listVersion int64
	digest      [sha256.Size]byte
}

func newAuthCache() *authCache {
	return &authCache{entries: make(map[authKey]time.Time)}
}

func makeAuthKey(listID int64, listVersion time.Time, username, password string) authKey {
	h := sha256.New()
	// Length-prefixed so ("ab", "c") and ("a", "bc") cannot collide.
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(username)))
	h.Write(n[:])
	h.Write([]byte(username))
	h.Write([]byte(password))

	key := authKey{listID: listID, listVersion: listVersion.UnixNano()}
	copy(key.digest[:], h.Sum(nil))
	return key
}

// granted reports whether this exact credential was verified recently.
func (c *authCache) granted(key authKey, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	expiry, found := c.entries[key]
	if !found {
		return false
	}
	if now.After(expiry) {
		delete(c.entries, key)
		return false
	}
	return true
}

// remember records a verified credential.
func (c *authCache) remember(key authKey, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= authCacheMax {
		c.evictExpiredLocked(now)
		// Still full of live entries: drop the cache rather than grow past
		// the cap. Losing it costs one hash per client, not correctness.
		if len(c.entries) >= authCacheMax {
			c.entries = make(map[authKey]time.Time)
		}
	}
	c.entries[key] = now.Add(authTTL)
}

func (c *authCache) evictExpiredLocked(now time.Time) {
	for key, expiry := range c.entries {
		if now.After(expiry) {
			delete(c.entries, key)
		}
	}
}

// size reports the entry count, for tests.
func (c *authCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
