package domain

import (
	"strconv"
	"strings"
)

// Location routes one path prefix of a host to its own backends.
//
// Without it a domain is one thing: ponzproxy matched on the Host header alone,
// so serving a frontend at / and an API at /api meant two subdomains, two
// certificates or a wildcard, a CORS policy, and cookies that no longer share
// an origin. That is a lot of accidental complexity to work around a routing
// table that only looked at one header.
//
// A location is deliberately not a whole host. It borrows the host's
// algorithm, health checks, TLS, access list, limits and inspection, because
// those are properties of the site rather than of a path — and because a
// location that could differ in all of them would be a host with extra steps.
type Location struct {
	ID     int64 `json:"id"`
	HostID int64 `json:"hostId"`
	// Path is the prefix this location claims, always starting with "/".
	// Matching is on whole segments: "/api" claims "/api" and "/api/v1"
	// but never "/apiary".
	Path string `json:"path"`
	// StripPrefix removes Path before the request is forwarded, so a
	// backend that serves "/v1/users" can sit behind "/api/v1/users"
	// without knowing it.
	StripPrefix bool       `json:"stripPrefix"`
	Upstreams   []Upstream `json:"upstreams"`
	// Position keeps the operator's ordering for display. Matching does
	// not depend on it: the longest path wins, whatever the order.
	Position int `json:"position"`
}

// MaxLocationsPerHost bounds one host's list. Past this it is a routing table,
// and a routing table wants a different design from a form.
const MaxLocationsPerHost = 32

// Matches reports whether a request path falls inside this location.
//
// The segment check is what stops "/api" claiming "/apiary". Getting that
// wrong is the classic prefix-routing bug: it silently sends one service's
// traffic to another, and only for paths nobody tested.
func (l *Location) Matches(path string) bool {
	if l.Path == "/" {
		return true
	}
	if !strings.HasPrefix(path, l.Path) {
		return false
	}
	rest := path[len(l.Path):]
	return rest == "" || strings.HasPrefix(rest, "/")
}

// Forward returns the path to send upstream.
func (l *Location) Forward(path string) string {
	if !l.StripPrefix || l.Path == "/" {
		return path
	}
	stripped := strings.TrimPrefix(path, l.Path)
	if stripped == "" {
		// A backend mounted at the root expects "/", not "". Sending the
		// empty string produces a request line of "GET  HTTP/1.1", which
		// some servers answer and others reject.
		return "/"
	}
	return stripped
}

// Normalize canonicalises the path and the upstreams beneath it.
func (l *Location) Normalize() {
	l.Path = strings.TrimSpace(l.Path)
	if l.Path == "" {
		l.Path = "/"
	}
	if !strings.HasPrefix(l.Path, "/") {
		l.Path = "/" + l.Path
	}
	// A trailing slash would make "/api/" fail to match "/api", which is
	// the one request every operator tries first.
	for len(l.Path) > 1 && strings.HasSuffix(l.Path, "/") {
		l.Path = l.Path[:len(l.Path)-1]
	}

	NormalizeUpstreams(l.Upstreams)
}

// Validate reports every problem at once.
func (l *Location) Validate(index int) error {
	v := &ValidationError{}
	field := func(name string) string {
		return "locations[" + strconv.Itoa(index) + "]." + name
	}

	if !strings.HasPrefix(l.Path, "/") {
		v.Add(field("path"), "must start with /")
	}
	if strings.ContainsAny(l.Path, " \t\r\n") {
		v.Add(field("path"), "must not contain spaces")
	}
	if strings.Contains(l.Path, "//") {
		v.Add(field("path"), "must not contain an empty segment")
	}
	if l.Path == "/" {
		// The host's own upstreams already serve everything unmatched, so
		// a location at "/" would be a second, shadowing definition of the
		// same thing.
		v.Add(field("path"), `use the host's own upstreams for "/" rather than a location`)
	}
	if len(l.Upstreams) == 0 {
		v.Add(field("upstreams"), "add at least one upstream, or remove the location")
	}
	// The same rules as a host's own upstreams, reported under this
	// location's prefix so the form can mark the right row.
	ValidateUpstreams(v, "locations["+strconv.Itoa(index)+"].upstreams", l.Upstreams)
	return v.Err()
}
