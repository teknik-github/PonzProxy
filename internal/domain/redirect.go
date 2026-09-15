package domain

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RedirectStatuses lists the status codes a redirect may answer with, in the
// order the UI offers them.
//
// Only these four are allowed. 301 and 302 are what browsers have always
// understood, but both are specified to let a client turn a POST into a GET
// and drop the body — every browser does exactly that. 307 and 308 were added
// precisely to forbid that rewrite, so they are the only safe choice when the
// redirected domain serves an API, a form target or a webhook receiver.
// Offering all four lets an operator keep the familiar caching behaviour
// (301/308 permanent, 302/307 temporary) while choosing whether method and
// body survive the hop.
func RedirectStatuses() []int { return []int{301, 302, 307, 308} }

// ValidRedirectStatus reports whether code is one of RedirectStatuses.
func ValidRedirectStatus(code int) bool {
	switch code {
	case 301, 302, 307, 308:
		return true
	}
	return false
}

// Redirect answers a set of domains with an HTTP redirect instead of proxying
// them. It is the counterpart to Host: same domain matching, no upstreams.
type Redirect struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	// Domains are matched against the request's Host header exactly as a
	// Host's are, including single-label "*." wildcards.
	Domains []string `json:"domains"`

	// Target is the absolute URL clients are sent to. Normalize turns a
	// bare domain into one, so operators can type "example.com".
	Target string `json:"target"`

	// StatusCode is one of RedirectStatuses.
	StatusCode int `json:"statusCode"`

	// PreservePath appends the request's path and query to Target, so
	// /a/b?c=d on the old domain lands on the same page of the new one.
	// With it off every request collapses onto Target itself.
	PreservePath bool `json:"preservePath"`

	// CertificateID, when set, terminates TLS for these domains so the
	// redirect can be served over HTTPS. Without it the domains are only
	// answered on the plaintext listener — which is enough for a domain
	// being retired, and useless for one that was previously HTTPS.
	CertificateID *int64 `json:"certificateId"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RedirectRepository stores redirects together with their domains. It lives
// here rather than in repository.go so that the redirect feature is one file
// to read; the rule it obeys is the same — the store depends on the domain.
//
// Create and Update return ErrConflict when one of the redirect's domains is
// already routed, whether by another redirect or by a Host: the two share a
// single domain namespace.
type RedirectRepository interface {
	// List returns every redirect, domains included, ordered by name.
	List(ctx context.Context) ([]Redirect, error)
	// Get returns ErrNotFound when no redirect has the given id.
	Get(ctx context.Context, id int64) (*Redirect, error)
	Create(ctx context.Context, rd *Redirect) error
	Update(ctx context.Context, rd *Redirect) error
	Delete(ctx context.Context, id int64) error
	// CountByCertificate reports how many redirects reference a
	// certificate, so deleting one still in use can be refused.
	CountByCertificate(ctx context.Context, certID int64) (int, error)
}

// DefaultRedirectStatus is applied to a redirect that does not choose one.
// 301 is what operators mean by "this domain moved", and is cached by clients
// so the old domain stops being asked at all.
const DefaultRedirectStatus = 301

// Normalize fills in defaults and canonicalises operator input. As with Host,
// it is always safe to call twice.
func (r *Redirect) Normalize() {
	r.Name = strings.TrimSpace(r.Name)

	domains := make([]string, 0, len(r.Domains))
	seen := make(map[string]struct{}, len(r.Domains))
	for _, d := range r.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimSuffix(d, ".")
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		domains = append(domains, d)
	}
	r.Domains = domains

	if r.StatusCode == 0 {
		r.StatusCode = DefaultRedirectStatus
	}
	r.Target = normalizeTarget(r.Target)
}

// normalizeTarget turns whatever the operator typed into an absolute URL.
//
// A bare "example.com/new" is assumed to be https: a redirect that downgrades
// its visitors to plaintext is almost never what was meant, and an operator
// who really wants http can say so. The scheme and host are lowercased; the
// path is not, because paths are case-sensitive on most servers.
func normalizeTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}

	// A leading "//" is protocol-relative. Resolving it here means the
	// stored target always carries an explicit scheme, so nothing further
	// down has to reason about what a schemeless Location header does.
	if rest, ok := strings.CutPrefix(target, "//"); ok {
		target = "https://" + rest
	} else if !hasURLScheme(target) {
		target = "https://" + target
	}

	u, err := url.Parse(target)
	if err != nil {
		// Leave it alone; Validate reports the parse failure with the
		// original text, which is what the operator needs to see.
		return target
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String()
}

// hasURLScheme reports whether s starts with "scheme:".
//
// It is deliberately permissive about which scheme — "javascript:" matches —
// so that Validate, not Normalize, is what rejects one ponzproxy will not
// emit. It is strict about what is *not* a scheme, because "example.com:8443"
// and "localhost:8080" have to keep reading as a host and a port: a dot before
// the colon, or digits after it, means an authority rather than a scheme.
func hasURLScheme(s string) bool {
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return false
	}
	for _, c := range s[:colon] {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '+', c == '-':
		default:
			return false
		}
	}
	rest, _, _ := strings.Cut(s[colon+1:], "/")
	return rest == "" || !isAllDigits(rest)
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Validate reports every problem with the redirect at once. Call Normalize
// first.
func (r *Redirect) Validate() error {
	v := &ValidationError{}

	if r.Name == "" {
		v.Add("name", "is required")
	} else if len(r.Name) > 100 {
		v.Add("name", "must be at most 100 characters")
	}

	if len(r.Domains) == 0 {
		v.Add("domains", "at least one domain is required")
	}
	for i, d := range r.Domains {
		if err := validateDomain(d); err != nil {
			v.Add("domains["+strconv.Itoa(i)+"]", "%s", err.Error())
		}
	}

	if !ValidRedirectStatus(r.StatusCode) {
		v.Add("statusCode",
			"must be 301, 302, 307 or 308; 307 and 308 are the ones that keep the method and body")
	}

	r.validateTarget(v)

	// A redirect answering its own domains would loop the client until the
	// browser gives up, and it is an easy mistake when editing an existing
	// record's target.
	// MatchesDomain rather than a string compare, so a wildcard catches the
	// loop too: "*.example.com" -> "https://www.example.com" is one.
	if host := targetHost(r.Target); host != "" && r.MatchesDomain(host) {
		v.Add("target", "points back at %s, which this redirect already answers for", host)
	}

	return v.Err()
}

// validateTarget rejects anything ponzproxy must not put in a Location header.
//
// The target is operator-supplied rather than attacker-supplied, so this is
// not an open-redirect defence in the usual sense; it is there to stop a
// mistyped or pasted value from turning the redirect into something worse than
// a broken link. Only http and https are emitted: a "javascript:" or "data:"
// Location is executed in the visitor's browser by some clients, which would
// make every redirect record a stored-XSS slot for whoever can reach the
// control plane.
func (r *Redirect) validateTarget(v *ValidationError) {
	if r.Target == "" {
		v.Add("target", "is required")
		return
	}
	u, err := url.Parse(r.Target)
	if err != nil {
		v.Add("target", "is not a valid URL")
		return
	}
	switch u.Scheme {
	case "http", "https":
	default:
		v.Add("target", "must be an http or https URL")
		return
	}
	if u.Host == "" {
		v.Add("target", "is missing a domain")
		return
	}
	if u.Fragment != "" {
		// A fragment never reaches the server, so one in a stored target
		// is a misunderstanding worth naming rather than silently dropping.
		v.Add("target", "must not contain a #fragment; it is never sent to the target server")
	}
	if strings.ContainsAny(r.Target, "\r\n") {
		v.Add("target", "must not contain line breaks")
	}
}

// targetHost returns the host of a target URL, or "" when it has none.
func targetHost(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Location builds the absolute URL to send one request to.
//
// reqURL is the request's URL as the data plane received it; only its path and
// query are read. The result always carries an explicit scheme and host taken
// from Target, so no part of the request can change where the visitor is sent
// — appending a path of "//evil.example" produces a second slash inside the
// configured host's path, not a new authority.
func (r *Redirect) Location(reqURL *url.URL) string {
	target, err := url.Parse(r.Target)
	if err != nil || target.Host == "" {
		// Validate rules this out on the way in; a row that predates a
		// rule is still better answered with the raw target than with a
		// half-built URL.
		return r.Target
	}

	base := target.Scheme + "://" + target.Host
	if !r.PreservePath {
		return base + target.EscapedPath() + rawQuery(target.RawQuery)
	}

	// The target's own path becomes a prefix: "https://new.example/app"
	// plus a request for "/a/b" is "https://new.example/app/a/b". The
	// trailing slash is trimmed so the two never collide into "//".
	prefix := strings.TrimSuffix(target.EscapedPath(), "/")
	suffix := sanitizePath(reqURL.EscapedPath())
	if suffix == "/" && prefix != "" {
		// "/app" + "/" would leave a trailing slash the operator did not
		// ask for; the prefix on its own is the same resource.
		suffix = ""
	}
	return base + prefix + suffix + rawQuery(mergeQuery(target.RawQuery, reqURL.RawQuery))
}

// sanitizePath drops control characters from a request path before it is
// echoed into a Location header. net/http sanitises header values too, but a
// redirect is the one place where request-controlled bytes are written back
// into a response header, so this does not rely on that alone.
func sanitizePath(p string) string {
	if p == "" {
		return ""
	}
	if !strings.ContainsFunc(p, isControl) {
		if p[0] != '/' {
			return "/" + p
		}
		return p
	}
	var b strings.Builder
	b.Grow(len(p))
	for _, c := range p {
		if !isControl(c) {
			b.WriteRune(c)
		}
	}
	out := b.String()
	if out != "" && out[0] != '/' {
		out = "/" + out
	}
	return out
}

func isControl(c rune) bool { return c < 0x20 || c == 0x7f }

// mergeQuery keeps both the target's fixed query and the request's, so a
// target of "?utm_source=old" does not silently discard "?page=2".
func mergeQuery(targetQuery, requestQuery string) string {
	switch {
	case targetQuery == "":
		return requestQuery
	case requestQuery == "":
		return targetQuery
	}
	return targetQuery + "&" + requestQuery
}

func rawQuery(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

// MatchesDomain reports whether this redirect answers for a hostname,
// honouring single-label wildcards the same way Host routing does.
func (r *Redirect) MatchesDomain(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for _, d := range r.Domains {
		if d == name {
			return true
		}
		if suffix, ok := strings.CutPrefix(d, "*."); ok {
			if idx := strings.IndexByte(name, '.'); idx >= 0 && name[idx+1:] == suffix {
				return true
			}
		}
	}
	return false
}
