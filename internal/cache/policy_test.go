package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

func testConfig() *domain.Cache {
	c := domain.DefaultCache()
	c.Enabled = true
	c.Paths = []string{".js", ".css", ".png", "/assets/"}
	c.Normalize()
	return &c
}

func get(target string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://example.com"+target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}
	return r
}

func header(pairs ...string) http.Header {
	h := make(http.Header)
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Add(pairs[i], pairs[i+1])
	}
	if h.Get("Content-Length") == "" {
		h.Set("Content-Length", "11")
	}
	return h
}

func TestEligible(t *testing.T) {
	cfg := testConfig()

	cases := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{"asset by extension", get("/app.js"), true},
		{"asset by prefix", get("/assets/whatever"), true},
		{"asset with a query", get("/app.js?v=3"), true},
		{"head is safe", httptest.NewRequest(http.MethodHead, "/app.js", nil), true},
		{"post is not", httptest.NewRequest(http.MethodPost, "/app.js", nil), false},
		{"put is not", httptest.NewRequest(http.MethodPut, "/app.js", nil), false},
		{"delete is not", httptest.NewRequest(http.MethodDelete, "/app.js", nil), false},
		{"patch is not", httptest.NewRequest(http.MethodPatch, "/app.js", nil), false},
		{"unlisted path", get("/api/orders"), false},
		{"range request", get("/app.js", "Range", "bytes=0-99"), false},
		{"client asked for no stored copy", get("/app.js", "Cache-Control", "no-store"), false},
		{"client no-cache still allows storing", get("/app.js", "Cache-Control", "no-cache"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Eligible(cfg, tc.req); got != tc.want {
				t.Fatalf("Eligible = %v, want %v", got, tc.want)
			}
		})
	}

	off := *cfg
	off.Enabled = false
	if Eligible(&off, get("/app.js")) {
		t.Error("a disabled cache must never be eligible")
	}
}

// TestStorable is the cacheability matrix. Every row is a rule the origin or
// the request imposes, and the origin's refusals must beat the host's setting.
func TestStorable(t *testing.T) {
	cfg := testConfig()
	now := time.Now()

	cases := []struct {
		name   string
		req    *http.Request
		status int
		header http.Header
		want   bool
	}{
		{"plain asset", get("/app.js"), 200, header(), true},
		{"explicit max-age", get("/app.js"), 200, header("Cache-Control", "max-age=600"), true},
		{"public", get("/app.js"), 200, header("Cache-Control", "public"), true},

		// Statuses.
		{"204", get("/app.js"), 204, header("Content-Length", "0"), false},
		{"301", get("/app.js"), 301, header(), false},
		{"304", get("/app.js"), 304, header(), false},
		{"404", get("/app.js"), 404, header(), false},
		{"500", get("/app.js"), 500, header(), false},
		{"206 partial", get("/app.js"), 206, header(), false},

		// Methods: only a GET produces a body worth storing.
		{"head has no body to store", httptest.NewRequest(http.MethodHead, "/app.js", nil), 200, header(), false},

		// The origin's directives win over the host's setting.
		{"no-store", get("/app.js"), 200, header("Cache-Control", "no-store"), false},
		{"no-cache", get("/app.js"), 200, header("Cache-Control", "no-cache"), false},
		{"private", get("/app.js"), 200, header("Cache-Control", "private, max-age=600"), false},
		{"no-store among others", get("/app.js"), 200, header("Cache-Control", "public, max-age=600, no-store"), false},
		{"max-age=0", get("/app.js"), 200, header("Cache-Control", "max-age=0"), false},
		{"s-maxage=0 beats max-age", get("/app.js"), 200, header("Cache-Control", "max-age=600, s-maxage=0"), false},
		{"pragma no-cache alone", get("/app.js"), 200, header("Pragma", "no-cache"), false},
		{"cache-control beats pragma", get("/app.js"), 200, header("Pragma", "no-cache", "Cache-Control", "max-age=60"), true},
		{"split across two headers", get("/app.js"), 200, header("Cache-Control", "max-age=60", "Cache-Control", "no-store"), false},

		// Expires.
		{"expires in the future", get("/app.js"), 200,
			header("Expires", now.Add(time.Hour).UTC().Format(http.TimeFormat)), true},
		{"expires in the past", get("/app.js"), 200,
			header("Expires", now.Add(-time.Hour).UTC().Format(http.TimeFormat)), false},
		{"unparseable expires means expired", get("/app.js"), 200, header("Expires", "0"), false},

		// Credentials.
		{"authorization without public", get("/app.js", "Authorization", "Bearer x"), 200, header("Cache-Control", "max-age=600"), false},
		{"authorization with public", get("/app.js", "Authorization", "Bearer x"), 200, header("Cache-Control", "public, max-age=600"), true},
		{"cookie without public", get("/app.js", "Cookie", "sid=1"), 200, header(), false},
		{"cookie with public", get("/app.js", "Cookie", "sid=1"), 200, header("Cache-Control", "public"), true},

		// Per-response refusals.
		{"response sets a cookie", get("/app.js"), 200, header("Set-Cookie", "sid=1"), false},
		{"vary star", get("/app.js"), 200, header("Vary", "*"), false},
		{"vary on cookie", get("/app.js"), 200, header("Vary", "Cookie"), false},
		{"vary on accept-encoding is keyed", get("/app.js"), 200, header("Vary", "Accept-Encoding"), true},
		{"vary on encoding plus another", get("/app.js"), 200, header("Vary", "Accept-Encoding, User-Agent"), false},

		// Framing.
		{"no content-length", get("/app.js"), 200, http.Header{}, false},
		{"unparseable content-length", get("/app.js"), 200, header("Content-Length", "big"), false},
		{"larger than one object may be", get("/app.js"), 200, header("Content-Length", "99999999"), false},
		{"exactly at the object limit", get("/app.js"), 200,
			header("Content-Length", "1048576"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := Storable(cfg, tc.req, tc.status, tc.header, now)
			if got != tc.want {
				t.Fatalf("Storable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStorableFreshness(t *testing.T) {
	cfg := testConfig()
	cfg.TTL = 5 * time.Minute
	cfg.MaxTTL = time.Hour
	now := time.Now()

	cases := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"falls back to the host default", header(), 5 * time.Minute},
		{"origin max-age", header("Cache-Control", "max-age=900"), 15 * time.Minute},
		{"s-maxage wins for a shared cache", header("Cache-Control", "max-age=60, s-maxage=900"), 15 * time.Minute},
		{"origin is clamped to maxTtl", header("Cache-Control", "max-age=31536000"), time.Hour},
		{"quoted delta", header("Cache-Control", `max-age="120"`), 2 * time.Minute},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := Storable(cfg, get("/app.js"), 200, tc.header, now)
			if !ok {
				t.Fatal("expected the response to be storable")
			}
			if got := s.ExpiresAt.Sub(now); got != tc.want {
				t.Fatalf("freshness = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServable(t *testing.T) {
	shared := &Entry{Public: false}
	public := &Entry{Public: true}

	cases := []struct {
		name  string
		entry *Entry
		req   *http.Request
		want  bool
	}{
		{"plain request", shared, get("/app.js"), true},
		{"client no-cache", shared, get("/app.js", "Cache-Control", "no-cache"), false},
		{"client pragma no-cache", shared, get("/app.js", "Pragma", "no-cache"), false},
		{"client no-store", shared, get("/app.js", "Cache-Control", "no-store"), false},
		{"authorization against a shared copy", shared, get("/app.js", "Authorization", "Bearer x"), false},
		{"authorization against a public copy", public, get("/app.js", "Authorization", "Bearer x"), true},
		{"cookie against a shared copy", shared, get("/app.js", "Cookie", "sid=1"), false},
		{"cookie against a public copy", public, get("/app.js", "Cookie", "sid=1"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Servable(tc.entry, tc.req); got != tc.want {
				t.Fatalf("Servable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNotModified(t *testing.T) {
	modified := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	e := &Entry{
		ETag:         `"abc123"`,
		LastModified: modified.Format(http.TimeFormat),
	}

	cases := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{"unconditional", get("/app.js"), false},
		{"matching etag", get("/app.js", "If-None-Match", `"abc123"`), true},
		{"weak form of the same etag", get("/app.js", "If-None-Match", `W/"abc123"`), true},
		{"one of several", get("/app.js", "If-None-Match", `"x", "abc123"`), true},
		{"wildcard", get("/app.js", "If-None-Match", "*"), true},
		{"different etag", get("/app.js", "If-None-Match", `"other"`), false},
		{"etag beats a stale date", get("/app.js",
			"If-None-Match", `"other"`,
			"If-Modified-Since", modified.Format(http.TimeFormat)), false},
		{"same instant", get("/app.js", "If-Modified-Since", modified.Format(http.TimeFormat)), true},
		{"client has a newer copy", get("/app.js",
			"If-Modified-Since", modified.Add(time.Hour).Format(http.TimeFormat)), true},
		{"client has an older copy", get("/app.js",
			"If-Modified-Since", modified.Add(-time.Hour).Format(http.TimeFormat)), false},
		{"unparseable date", get("/app.js", "If-Modified-Since", "yesterday"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NotModified(e, tc.req); got != tc.want {
				t.Fatalf("NotModified = %v, want %v", got, tc.want)
			}
		})
	}

	// An entry with no validators can never answer a conditional request.
	bare := &Entry{}
	if NotModified(bare, get("/app.js", "If-None-Match", `"abc123"`)) {
		t.Error("an entry without an ETag matched If-None-Match")
	}
}

func TestParseAcceptEncoding(t *testing.T) {
	cases := []struct {
		value string
		want  Encodings
	}{
		{"", 0},
		{"identity", 0},
		{"gzip", EncGzip},
		{"GZIP", EncGzip},
		{"gzip, deflate, br", EncGzip | EncDeflate | EncBrotli},
		{"gzip, deflate, br, zstd", EncGzip | EncDeflate | EncBrotli | EncZstd},
		{"br;q=1.0, gzip;q=0.8", EncBrotli | EncGzip},
		{"gzip;q=0", 0},
		{"gzip;q=0.000", 0},
		{"gzip;q=0, br", EncBrotli},
		{"x-gzip", EncGzip},
		{"*", 0},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			if got := ParseAcceptEncoding(tc.value); got != tc.want {
				t.Fatalf("ParseAcceptEncoding(%q) = %b, want %b", tc.value, got, tc.want)
			}
		})
	}
}

// TestCopyStorableHeaderDropsPerConnectionFields guards the rule that a stored
// response is replayed to other clients: anything describing one connection,
// or one visitor, must not come with it.
func TestCopyStorableHeaderDropsPerConnectionFields(t *testing.T) {
	in := http.Header{}
	in.Set("Content-Type", "application/javascript")
	in.Set("Etag", `"abc"`)
	in.Set("Set-Cookie", "sid=secret")
	in.Set("Connection", "Keep-Alive, X-Private")
	in.Set("Keep-Alive", "timeout=5")
	in.Set("Transfer-Encoding", "chunked")
	in.Set("Proxy-Authenticate", "Basic")
	in.Set("Upgrade", "h2c")
	in.Set("X-Private", "leaked")
	in.Set("Content-Length", "123")
	in.Set("X-Cache", "MISS")
	in.Set("Age", "17")

	out := CopyStorableHeader(in)

	for _, gone := range []string{
		"Set-Cookie", "Connection", "Keep-Alive", "Transfer-Encoding",
		"Proxy-Authenticate", "Upgrade", "X-Private", "Content-Length",
		"X-Cache", "Age",
	} {
		if out.Get(gone) != "" {
			t.Errorf("%s survived into the stored entry", gone)
		}
	}
	if out.Get("Content-Type") != "application/javascript" || out.Get("Etag") != `"abc"` {
		t.Fatalf("useful headers were dropped: %v", out)
	}

	// The copy must be independent: the response is still being written when
	// the entry is built.
	in.Set("Content-Type", "text/plain")
	if out.Get("Content-Type") != "application/javascript" {
		t.Error("the stored header aliases the live one")
	}
}

func TestInvalidates(t *testing.T) {
	cfg := testConfig()

	cases := []struct {
		method string
		target string
		want   bool
	}{
		{http.MethodGet, "/app.js", false},
		{http.MethodHead, "/app.js", false},
		{http.MethodOptions, "/app.js", false},
		{http.MethodTrace, "/app.js", false},
		{http.MethodPost, "/app.js", true},
		{http.MethodPut, "/app.js", true},
		{http.MethodPatch, "/app.js", true},
		{http.MethodDelete, "/app.js", true},
		// A write to a path the cache never holds has nothing to drop.
		{http.MethodPost, "/api/orders", false},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.target, nil)
			if got := Invalidates(cfg, r); got != tc.want {
				t.Fatalf("Invalidates = %v, want %v", got, tc.want)
			}
		})
	}

	off := *cfg
	off.Enabled = false
	if Invalidates(&off, httptest.NewRequest(http.MethodPost, "/app.js", nil)) {
		t.Error("a disabled cache has nothing to invalidate")
	}
}
