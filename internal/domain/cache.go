package domain

import (
	"strings"
	"time"
)

// Cache is the per-host static asset cache setting.
//
// It is off by default and, when on, applies only to the paths an operator
// lists. Caching everything a host serves would be a correctness hazard on a
// proxy that cannot see inside the application: the first personalised page
// stored under a shared key is served to the next visitor. Naming the paths
// keeps the feature to what it is actually for — bundles, images and fonts —
// and makes the blast radius of a mistake something an operator chose.
type Cache struct {
	Enabled bool `json:"enabled"`

	// Paths selects what may be cached. An entry beginning with "." is an
	// extension matched against the end of the request path (".js"); any
	// other entry is a path prefix ("/assets/"). Both forms exist because
	// asset layouts come in both shapes, and neither alone covers a
	// hashed-filename bundle sitting next to an API under the same host.
	Paths []string `json:"paths"`

	// TTL is how long a response stays fresh when the origin says nothing
	// about freshness. It is the one guess this cache makes, which is why
	// it is short by default and why the feature is opt-in per path.
	TTL time.Duration `json:"ttl"`
	// MaxTTL clamps the freshness an origin asks for. An asset sent with
	// "max-age=31536000" would otherwise sit in a bounded cache for a year,
	// holding bytes that newer objects need, and would survive any mistake
	// in it for just as long.
	MaxTTL time.Duration `json:"maxTtl"`

	// MaxObjectBytes refuses to buffer a response larger than this. Without
	// it one video file would consume the whole budget and evict every
	// object that was actually earning its place.
	MaxObjectBytes int64 `json:"maxObjectBytes"`
	// MaxBytes is the host's total budget. A cache in a proxy with no upper
	// bound is an out-of-memory failure waiting for enough distinct URLs.
	MaxBytes int64 `json:"maxBytes"`
}

// DefaultCachePaths is the starting selection offered to a new host: the
// extensions a build tool emits with content hashes in their names, which are
// the safest possible thing to cache, plus the two directory prefixes those
// files conventionally live under.
func DefaultCachePaths() []string {
	return []string{
		".js", ".mjs", ".css", ".woff", ".woff2", ".ico",
		".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".avif",
		"/assets/", "/static/",
	}
}

// DefaultCache is applied to new hosts. It is off: switching a cache on for
// someone's live traffic without being asked turns a stale response into the
// proxy's fault, and the operator has no reason to suspect the proxy.
func DefaultCache() Cache {
	return Cache{
		Enabled:        false,
		Paths:          DefaultCachePaths(),
		TTL:            5 * time.Minute,
		MaxTTL:         24 * time.Hour,
		MaxObjectBytes: 1 << 20,  // 1 MiB
		MaxBytes:       64 << 20, // 64 MiB
	}
}

// Active reports whether this host caches anything at all. A cache with no
// paths can never match, so it is treated as off rather than as a cache that
// silently does nothing.
func (c *Cache) Active() bool { return c.Enabled && len(c.Paths) > 0 }

// Matches reports whether a request path is one the operator asked to cache.
//
// Extensions are compared without regard to case because a URL path is
// case-sensitive in general but file extensions in practice are not, and
// EqualFold does it without allocating a lowered copy of the path on the
// request path. Prefixes are compared exactly: a prefix names a location on
// the origin, and folding case there would widen the selection beyond what was
// written.
func (c *Cache) Matches(path string) bool {
	for _, p := range c.Paths {
		if len(p) > 0 && p[0] == '.' {
			if len(path) >= len(p) && strings.EqualFold(path[len(path)-len(p):], p) {
				return true
			}
			continue
		}
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Normalize fills in defaults and canonicalises operator input.
func (c *Cache) Normalize() {
	paths := make([]string, 0, len(c.Paths))
	seen := make(map[string]struct{}, len(c.Paths))
	for _, p := range c.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Extensions are stored lowered so the list reads consistently in
		// the console; Matches folds case anyway.
		if p[0] == '.' {
			p = strings.ToLower(p)
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	c.Paths = paths

	if c.TTL <= 0 {
		c.TTL = 5 * time.Minute
	}
	if c.MaxTTL <= 0 {
		c.MaxTTL = 24 * time.Hour
	}
	if c.MaxObjectBytes <= 0 {
		c.MaxObjectBytes = 1 << 20
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = 64 << 20
	}
}

// Validate checks the setting as a whole. Call Normalize first.
func (c *Cache) Validate() error {
	v := &ValidationError{}

	if c.Enabled && len(c.Paths) == 0 {
		v.Add("cache.paths", "choose at least one path or extension, or nothing can ever be cached")
	}
	if len(c.Paths) > 64 {
		v.Add("cache.paths", "must be at most 64 entries")
	}
	for _, p := range c.Paths {
		switch {
		case len(p) > 128:
			v.Add("cache.paths", "%q is longer than 128 characters", p)
		case p[0] != '.' && p[0] != '/':
			v.Add("cache.paths", "%q must be an extension like .js or a path prefix like /assets/", p)
		case p == ".":
			v.Add("cache.paths", "%q is not an extension", p)
		case p == "/":
			// "/" is a prefix of every path, which is the "cache
			// everything" this setting exists to avoid.
			v.Add("cache.paths", "%q matches every request; name the asset paths instead", p)
		}
	}

	if c.TTL < time.Second || c.TTL > 24*time.Hour {
		v.Add("cache.ttl", "must be between 1 second and 24 hours")
	}
	if c.MaxTTL < c.TTL {
		v.Add("cache.maxTtl", "must not be shorter than the default TTL")
	}
	if c.MaxTTL > 30*24*time.Hour {
		v.Add("cache.maxTtl", "must be at most 30 days")
	}

	if c.MaxObjectBytes < 1024 || c.MaxObjectBytes > 64<<20 {
		v.Add("cache.maxObjectBytes", "must be between 1 KiB and 64 MiB")
	}
	if c.MaxBytes < c.MaxObjectBytes {
		v.Add("cache.maxBytes", "must be at least as large as one object")
	}
	if c.MaxBytes > 1<<30 {
		v.Add("cache.maxBytes", "must be at most 1 GiB")
	}

	return v.Err()
}
