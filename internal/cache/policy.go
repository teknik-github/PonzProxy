package cache

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// Encodings is the set of content codings a request said it would accept. It
// is part of the cache key; see Key.
type Encodings uint8

const (
	// EncGzip and the rest are the four codings worth distinguishing. An
	// unrecognised coding collapses into the zero value, which is correct
	// but conservative: the cache simply keys such requests together with
	// clients that accept nothing.
	EncGzip Encodings = 1 << iota
	EncBrotli
	EncDeflate
	EncZstd
)

// ParseAcceptEncoding summarises an Accept-Encoding header.
//
// A coding offered with "q=0" is explicitly refused, so it must not end up in
// the key: a client that says "gzip;q=0" would otherwise share a key with one
// that wants gzip and be handed a body it cannot read.
func ParseAcceptEncoding(value string) Encodings {
	var enc Encodings
	if value == "" {
		return enc
	}
	for part := range strings.SplitSeq(value, ",") {
		token, params, _ := strings.Cut(part, ";")
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if refusedByQuality(params) {
			continue
		}
		switch {
		case strings.EqualFold(token, "gzip"), strings.EqualFold(token, "x-gzip"):
			enc |= EncGzip
		case strings.EqualFold(token, "br"):
			enc |= EncBrotli
		case strings.EqualFold(token, "deflate"):
			enc |= EncDeflate
		case strings.EqualFold(token, "zstd"):
			enc |= EncZstd
		}
	}
	return enc
}

// refusedByQuality reports whether the parameters carry a zero quality value.
// Anything other than a recognisable zero is treated as accepted, because the
// cost of being wrong in that direction is a duplicate cache entry rather than
// an unreadable response.
func refusedByQuality(params string) bool {
	for p := range strings.SplitSeq(params, ";") {
		name, value, found := strings.Cut(p, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return err == nil && q == 0
	}
	return false
}

// field reads a header by its canonical name.
//
// net/http's Get canonicalises its argument on every call, which the profile
// showed dominating the cache's own work: the hit path reads a dozen fields
// and none of them needed converting, because every key below is already
// written in canonical form and every header map the proxy sees was built by
// net/http, which canonicalises on parse.
func field(h http.Header, canonicalKey string) string {
	if vs := h[canonicalKey]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// hopByHop are the headers that describe one connection rather than the
// response, and so must never be replayed to a different client. Connection
// itself may also name further headers; those are dropped too.
var hopByHop = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// CopyStorableHeader returns the headers worth keeping with a cached response.
//
// Set-Cookie is dropped rather than treated as a reason to refuse the whole
// response, because the two decisions are separate: Storable already refuses
// anything carrying one. Dropping it here as well means that if that rule is
// ever relaxed, the failure mode is a missing cookie and not one visitor's
// session handed to another.
func CopyStorableHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		if skipHeader(h, k) {
			continue
		}
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func skipHeader(h http.Header, key string) bool {
	if strings.EqualFold(key, "Set-Cookie") {
		return true
	}
	for _, hop := range hopByHop {
		if strings.EqualFold(key, hop) {
			return true
		}
	}
	// Content-Length is regenerated when the entry is served, so keeping
	// the origin's copy only risks it disagreeing with the stored body.
	// X-Cache and Age describe one delivery of the response rather than the
	// response, so they are regenerated too.
	switch {
	case strings.EqualFold(key, "Content-Length"),
		strings.EqualFold(key, "X-Cache"),
		strings.EqualFold(key, "Age"):
		return true
	}
	for _, v := range h.Values("Connection") {
		for name := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(name), key) {
				return true
			}
		}
	}
	return false
}

// Eligible reports whether a request may take part in caching at all — be
// answered from the cache, or have its response stored.
//
// Only GET and HEAD qualify. A cache that answers an unsafe method would be
// answering the wrong question, and a proxy that cached the *result* of one
// would have to track invalidation across every path the application touches,
// which it cannot see. A Range request is excluded for a plainer reason: it
// asks for part of an object, and serving part of a cached one means
// implementing range arithmetic for no gain on the assets this cache is for.
func Eligible(cfg *domain.Cache, r *http.Request) bool {
	if !cfg.Active() {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if field(r.Header, "Range") != "" {
		return false
	}
	// A client asking for no stored copy at all is honoured in both
	// directions; "no-cache" is not, because it only forbids serving.
	if requestDirectives(r).noStore {
		return false
	}
	return cfg.Matches(r.URL.Path)
}

// Invalidates reports whether a request should drop any stored copy of its own
// path before it is forwarded.
//
// An unsafe method aimed at a cached path is the one case where a proxy can
// see that its copy is about to be wrong. Dropping it before the request is
// forwarded rather than after is deliberate: the request may not come back at
// all, and a cache holding a copy the origin has already replaced is the
// failure this exists to avoid. The cost of being early is one re-fetch.
func Invalidates(cfg *domain.Cache, r *http.Request) bool {
	if !cfg.Active() {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	return cfg.Matches(r.URL.Path)
}

// Servable reports whether a stored entry may answer this particular request.
//
// Freshness is checked by the caller's lookup; what is decided here is whether
// this client is allowed to see a response that was produced for someone else.
func Servable(e *Entry, r *http.Request) bool {
	d := requestDirectives(r)
	if d.noStore || d.noCache {
		return false
	}
	// A request carrying credentials only gets a shared copy when the
	// origin explicitly said the response is public. Authorization is the
	// case the HTTP specification names; Cookie is included because a
	// session cookie is how nearly every real application personalises a
	// response, and a proxy cannot tell which ones do.
	if field(r.Header, "Authorization") != "" || field(r.Header, "Cookie") != "" {
		return e.Public
	}
	return true
}

// Storability is what Storable worked out about a response: how long it may be
// held, whether it may be shown to a credentialed client, and how many bytes
// the body will be.
type Storability struct {
	ExpiresAt time.Time
	Public    bool
	// Length is the origin's Content-Length. It is the only signal that a
	// body arrived complete — see Storable.
	Length int64
}

// Storable decides whether a response may be stored, and on what terms.
//
// The origin always wins. A host can be configured to cache every asset it
// serves and a single "Cache-Control: no-store" from the application still
// keeps that response out: the application knows what is in the body and the
// proxy does not.
//
// Only 200 is admitted. A 206 would need range arithmetic; a 301 or a 404 can
// be cached in principle, but a wrong one outlives the deploy that caused it
// and the win on an asset path is negligible. Success only, and the narrowest
// success at that.
//
// A response with no Content-Length is refused, which is the single decision
// that makes buffering safe. Without a framing length there is no way to tell
// a body the origin finished sending from one it cut short halfway, and a
// truncated asset served from cache for the rest of its TTL is a worse outcome
// than never caching it. Chunked responses therefore stream through untouched.
func Storable(cfg *domain.Cache, r *http.Request, status int, respHeader http.Header, now time.Time) (Storability, bool) {
	if r.Method != http.MethodGet || status != http.StatusOK {
		return Storability{}, false
	}
	// A body this cache has not been handed in full is not a body it can
	// replay. See the doc comment.
	length, err := strconv.ParseInt(field(respHeader, "Content-Length"), 10, 64)
	if err != nil || length < 0 || length > cfg.MaxObjectBytes {
		return Storability{}, false
	}
	if len(respHeader["Set-Cookie"]) > 0 {
		return Storability{}, false
	}
	if !varyIsCacheable(respHeader) {
		return Storability{}, false
	}

	cc := parseCacheControl(respHeader["Cache-Control"])
	if cc.noStore || cc.noCache || cc.private {
		return Storability{}, false
	}
	// Pragma is an HTTP/1.0 request header that origins nevertheless still
	// emit on responses they mean to keep out of caches. Honouring it costs
	// one lookup and respects the evident intent.
	if !cc.any && hasNoCachePragma(respHeader) {
		return Storability{}, false
	}

	if field(r.Header, "Authorization") != "" || field(r.Header, "Cookie") != "" {
		if !cc.public {
			return Storability{}, false
		}
	}

	ttl, ok := freshness(cc, respHeader, cfg, now)
	if !ok {
		return Storability{}, false
	}
	return Storability{ExpiresAt: now.Add(ttl), Public: cc.public, Length: length}, true
}

// freshness works out how long a response may be held. It reports false when
// the origin's own answer is "not at all", which is not the same as falling
// back to the configured default.
func freshness(cc cacheControl, respHeader http.Header, cfg *domain.Cache, now time.Time) (time.Duration, bool) {
	var ttl time.Duration
	switch {
	// s-maxage is addressed to shared caches specifically, which is what
	// this is, so it beats max-age when both are present.
	case cc.hasSMaxAge:
		ttl = cc.sMaxAge
	case cc.hasMaxAge:
		ttl = cc.maxAge
	case field(respHeader, "Expires") != "":
		exp, err := http.ParseTime(field(respHeader, "Expires"))
		if err != nil {
			// An unparseable Expires means "already expired" by the
			// specification, and treating it as absent would cache
			// something the origin was trying to keep out.
			return 0, false
		}
		ttl = exp.Sub(now)
	default:
		// No freshness information at all. This is the only guess the
		// cache makes, which is why it is the operator's number and why
		// the feature applies to named paths rather than everything.
		ttl = cfg.TTL
	}

	if ttl <= 0 {
		return 0, false
	}
	if ttl > cfg.MaxTTL {
		ttl = cfg.MaxTTL
	}
	return ttl, true
}

// varyIsCacheable reports whether the response varies only on things the key
// already covers.
//
// "Vary: *" means the response is unique to its request and can never be
// reused. Any other field the cache does not key on — Cookie, User-Agent,
// Accept-Language — would mean storing one client's variant and serving it to
// every other, so those responses are left alone rather than keyed on headers
// this cache would have to copy wholesale.
func varyIsCacheable(respHeader http.Header) bool {
	for _, v := range respHeader["Vary"] {
		for field := range strings.SplitSeq(v, ",") {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			if !strings.EqualFold(field, "Accept-Encoding") {
				return false
			}
		}
	}
	return true
}

// NotModified reports whether a conditional request can be answered with 304
// from this entry.
//
// If-None-Match wins over If-Modified-Since when both are sent, because an
// entity tag is an exact identity and a date is a one-second approximation of
// one.
func NotModified(e *Entry, r *http.Request) bool {
	if inm := field(r.Header, "If-None-Match"); inm != "" {
		return etagMatches(inm, e.ETag)
	}
	if ims := field(r.Header, "If-Modified-Since"); ims != "" && e.LastModified != "" {
		since, err := http.ParseTime(ims)
		if err != nil {
			return false
		}
		modified, err := http.ParseTime(e.LastModified)
		if err != nil {
			return false
		}
		// Truncated to the second on both sides already by the HTTP date
		// format, so equality means unmodified.
		return !modified.After(since)
	}
	return false
}

// etagMatches applies the weak comparison If-None-Match calls for: "W/" marks
// a tag as semantically rather than byte-for-byte equal, and for a
// revalidation that is exactly the question being asked.
func etagMatches(ifNoneMatch, stored string) bool {
	if stored == "" {
		return false
	}
	for candidate := range strings.SplitSeq(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if trimWeak(candidate) == trimWeak(stored) {
			return true
		}
	}
	return false
}

func trimWeak(tag string) string {
	if after, ok := strings.CutPrefix(tag, "W/"); ok {
		return after
	}
	return tag
}

// cacheControl is the subset of the directives this cache acts on.
//
// "must-revalidate" and "proxy-revalidate" are absent because they are already
// honoured: they forbid serving a stale response, and nothing here ever does.
type cacheControl struct {
	// any records that a Cache-Control header was present at all, so that
	// an HTTP/1.0 Pragma is only consulted when the origin said nothing
	// more modern.
	any        bool
	noStore    bool
	noCache    bool
	private    bool
	public     bool
	maxAge     time.Duration
	hasMaxAge  bool
	sMaxAge    time.Duration
	hasSMaxAge bool
}

func parseCacheControl(values []string) cacheControl {
	var cc cacheControl
	for _, value := range values {
		if value != "" {
			cc.any = true
		}
		for part := range strings.SplitSeq(value, ",") {
			name, arg, hasArg := strings.Cut(part, "=")
			name = strings.TrimSpace(name)
			switch {
			case strings.EqualFold(name, "no-store"):
				cc.noStore = true
			case strings.EqualFold(name, "no-cache"):
				// A qualified "no-cache=\"Set-Cookie\"" only
				// forbids reusing those fields, but this cache
				// stores whole responses, so it is treated the
				// same as the bare form.
				cc.noCache = true
			case strings.EqualFold(name, "private"):
				cc.private = true
			case strings.EqualFold(name, "public"):
				cc.public = true
			case strings.EqualFold(name, "max-age") && hasArg:
				if d, ok := parseSeconds(arg); ok {
					cc.maxAge, cc.hasMaxAge = d, true
				}
			case strings.EqualFold(name, "s-maxage") && hasArg:
				if d, ok := parseSeconds(arg); ok {
					cc.sMaxAge, cc.hasSMaxAge = d, true
				}
			}
		}
	}
	return cc
}

func parseSeconds(arg string) (time.Duration, bool) {
	arg = strings.Trim(strings.TrimSpace(arg), `"`)
	n, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	// Clamped well below the point where seconds overflow a Duration; a
	// value this large is a mistake or an attack on the arithmetic, and
	// either way MaxTTL is about to cut it down.
	const maxSeconds = 100 * 365 * 24 * 60 * 60
	if n > maxSeconds {
		n = maxSeconds
	}
	return time.Duration(n) * time.Second, true
}

// requestDirectives reads the client's own Cache-Control, plus the HTTP/1.0
// Pragma that older clients and some corporate proxies still send in its
// place.
func requestDirectives(r *http.Request) cacheControl {
	values := r.Header["Cache-Control"]
	// The overwhelming majority of requests send neither field, and leaving
	// early saves the whole tokenising pass on the path a cache hit takes
	// twice.
	if len(values) == 0 {
		if hasNoCachePragma(r.Header) {
			return cacheControl{noCache: true}
		}
		return cacheControl{}
	}

	cc := parseCacheControl(values)
	if !cc.any && hasNoCachePragma(r.Header) {
		cc.noCache = true
	}
	return cc
}

// hasNoCachePragma matches the HTTP/1.0 directive without lowering a copy of
// the value, since the header is usually absent and never long.
func hasNoCachePragma(h http.Header) bool {
	value := field(h, "Pragma")
	for len(value) >= len("no-cache") {
		if strings.EqualFold(value[:len("no-cache")], "no-cache") {
			return true
		}
		value = value[1:]
	}
	return false
}
