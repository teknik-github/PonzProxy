package domain

import (
	"errors"
	"testing"
	"time"
)

func testCache() Cache {
	c := DefaultCache()
	c.Enabled = true
	return c
}

func TestCacheMatches(t *testing.T) {
	c := testCache()
	c.Paths = []string{".js", ".css", "/assets/", "/static/img/"}

	hit := []string{
		"/app.js",
		"/bundle.4f2a9c.JS", // extensions fold case, as real servers do
		"/theme.css",
		"/assets/anything/at/all",
		"/assets/",
		"/static/img/logo.webp",
		// The prefix is case-sensitive, but the extension still matches.
		"/Assets/app.js",
	}
	for _, p := range hit {
		if !c.Matches(p) {
			t.Errorf("%q should be cacheable", p)
		}
	}

	miss := []string{
		"/",
		"/api/orders",
		"/jsonapi",    // merely ends in the same letters
		"/app.js.map", // a different extension
		"/static/img", // a prefix of the prefix is not the prefix
		"/ASSETS/logo.png",
	}
	for _, p := range miss {
		if c.Matches(p) {
			t.Errorf("%q should not be cacheable", p)
		}
	}
}

func TestCacheActiveNeedsPaths(t *testing.T) {
	c := testCache()
	c.Paths = nil
	if c.Active() {
		t.Error("a cache with no paths can never match and must not count as active")
	}
}

func TestCacheNormalize(t *testing.T) {
	c := Cache{
		Enabled: true,
		Paths:   []string{"  .JS  ", ".js", "", "/assets/", "/assets/", "  "},
	}
	c.Normalize()

	want := []string{".js", "/assets/"}
	if len(c.Paths) != len(want) {
		t.Fatalf("paths = %v, want %v", c.Paths, want)
	}
	for i := range want {
		if c.Paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", c.Paths, want)
		}
	}

	if c.TTL != 5*time.Minute || c.MaxTTL != 24*time.Hour {
		t.Errorf("zero durations were not defaulted: %v / %v", c.TTL, c.MaxTTL)
	}
	if c.MaxObjectBytes != 1<<20 || c.MaxBytes != 64<<20 {
		t.Errorf("zero limits were not defaulted: %d / %d", c.MaxObjectBytes, c.MaxBytes)
	}

	// Normalize must be safe to run twice; the API layer does exactly that.
	before := len(c.Paths)
	c.Normalize()
	if len(c.Paths) != before {
		t.Error("second Normalize changed the result")
	}
}

func TestCacheValidate(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*Cache)
		field string
	}{
		{"default is valid", func(*Cache) {}, ""},
		{"enabled without paths", func(c *Cache) { c.Paths = nil }, "cache.paths"},
		{"bare slash matches everything", func(c *Cache) { c.Paths = []string{"/"} }, "cache.paths"},
		{"path is neither extension nor prefix", func(c *Cache) { c.Paths = []string{"assets"} }, "cache.paths"},
		{"ttl too short", func(c *Cache) { c.TTL = time.Millisecond }, "cache.ttl"},
		{"ttl too long", func(c *Cache) { c.TTL = 48 * time.Hour }, "cache.ttl"},
		{"maxTtl below ttl", func(c *Cache) { c.MaxTTL = time.Second }, "cache.maxTtl"},
		{"maxTtl beyond a month", func(c *Cache) { c.MaxTTL = 365 * 24 * time.Hour }, "cache.maxTtl"},
		{"object limit too small", func(c *Cache) { c.MaxObjectBytes = 10 }, "cache.maxObjectBytes"},
		{"object limit too large", func(c *Cache) { c.MaxObjectBytes = 1 << 30 }, "cache.maxObjectBytes"},
		{"budget below one object", func(c *Cache) { c.MaxBytes = 2048; c.MaxObjectBytes = 4096 }, "cache.maxBytes"},
		{"budget beyond a gigabyte", func(c *Cache) { c.MaxBytes = 2 << 30 }, "cache.maxBytes"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testCache()
			tc.mut(&c)
			err := c.Validate()

			if tc.field == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a validation error")
			}
			var v *ValidationError
			if !errors.As(err, &v) {
				t.Fatalf("error is %T, want *ValidationError", err)
			}
			for _, f := range v.Fields {
				if f.Field == tc.field {
					return
				}
			}
			t.Fatalf("no error on %q; got %v", tc.field, v.Fields)
		})
	}
}

// TestDefaultCacheIsOff guards the decision that matters most: enabling a
// cache changes what visitors see, so it is never on because nobody looked.
func TestDefaultCacheIsOff(t *testing.T) {
	c := DefaultCache()
	if c.Enabled || c.Active() {
		t.Fatal("caching must be off for a new host")
	}
	if len(c.Paths) == 0 {
		t.Error("a new host should still start with a suggested path list")
	}
}
