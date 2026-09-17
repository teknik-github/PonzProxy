package domain

import (
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Algorithm selects which upstream receives the next request.
type Algorithm string

const (
	// RoundRobin cycles through healthy upstreams in order.
	RoundRobin Algorithm = "round_robin"
	// WeightedRoundRobin distributes in proportion to Upstream.Weight using
	// smooth weighted round robin, so weights 5/1 interleave rather than
	// sending five consecutive requests to the same backend.
	WeightedRoundRobin Algorithm = "weighted_round_robin"
	// LeastConnections picks the healthy upstream with the fewest in-flight
	// requests, breaking ties by weight.
	LeastConnections Algorithm = "least_connections"
	// IPHash maps a client IP to a fixed upstream, giving sticky sessions
	// without cookies.
	IPHash Algorithm = "ip_hash"
)

// Valid reports whether a is one of the supported algorithms.
func (a Algorithm) Valid() bool {
	switch a {
	case RoundRobin, WeightedRoundRobin, LeastConnections, IPHash:
		return true
	}
	return false
}

// Algorithms lists every supported algorithm, for the UI's select box.
func Algorithms() []Algorithm {
	return []Algorithm{RoundRobin, WeightedRoundRobin, LeastConnections, IPHash}
}

// Host is one virtual host: the domains it answers for, the upstreams behind
// it, and how traffic is spread across them.
type Host struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	// Domains are matched against the request's Host header (and the TLS
	// SNI). A leading "*." makes it a wildcard for exactly one label.
	Domains []string `json:"domains"`

	Algorithm Algorithm  `json:"algorithm"`
	Upstreams []Upstream `json:"upstreams"`

	// CertificateID, when set, enables TLS termination for this host.
	CertificateID *int64 `json:"certificateId"`
	// AccessListID, when set, gates inbound requests before an upstream is
	// chosen. See AccessList for the evaluation order.
	AccessListID *int64 `json:"accessListId"`
	// ForceHTTPS redirects plaintext requests to https before proxying.
	ForceHTTPS bool `json:"forceHttps"`
	// HSTSMaxAge, when > 0, sets Strict-Transport-Security on TLS responses.
	HSTSMaxAge int `json:"hstsMaxAge"`

	// WebSocketSupport allows Upgrade requests to pass through.
	WebSocketSupport bool `json:"websocketSupport"`
	// PreserveHost sends the original Host header upstream instead of
	// rewriting it to the upstream's address.
	PreserveHost bool `json:"preserveHost"`

	HealthCheck HealthCheck `json:"healthCheck"`
	// PassiveHealth ejects a backend that keeps refusing real traffic, using
	// what the proxy already learns on the request path.
	PassiveHealth PassiveHealth `json:"passiveHealth"`
	// AccessLog records this host's requests for later search. Off by
	// default; see AccessLogSettings.
	AccessLog AccessLogSettings `json:"accessLog"`
	// Guardian inspects requests for obvious attack patterns. Off by
	// default; see Guardian.
	Guardian Guardian `json:"guardian"`
	// Cache serves cacheable upstream responses for the paths an operator
	// lists from memory instead of the backend. Off by default; see Cache.
	Cache Cache `json:"cache"`
	// TrafficLimits bounds what one client address may ask of this host.
	// Off by default; see TrafficLimits.
	TrafficLimits TrafficLimits `json:"trafficLimits"`
	// Maintenance answers this host with a page instead of proxying it.
	Maintenance Maintenance `json:"maintenance"`
	// ErrorPages replaces what a visitor sees when no backend can be
	// reached. Off by default, falling back to the built-in wording.
	ErrorPages ErrorPages `json:"errorPages"`
	// UsageAlert warns when this host's traffic passes a budget.
	UsageAlert UsageAlert `json:"usageAlert"`
	// Locations route path prefixes of this host to their own backends.
	// Anything that matches none of them is served by Upstreams above.
	Locations []Location `json:"locations"`
	// Headers rewrites what this host sends upstream and what it returns.
	// A location's own rules are applied after these.
	Headers Headers `json:"headers"`
	// Compression compresses responses on the way to the visitor. Off by
	// default; see Compression.
	Compression Compression `json:"compression"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Upstream is a single backend server behind a Host.
type Upstream struct {
	ID     int64 `json:"id"`
	HostID int64 `json:"hostId"`

	// Scheme is http or https — the protocol used to reach the backend.
	Scheme string `json:"scheme"`
	// Address is the backend's host:port.
	Address string `json:"address"`

	// Weight biases WeightedRoundRobin and breaks LeastConnections ties.
	// Always >= 1 after validation.
	Weight int `json:"weight"`
	// MaxConns caps in-flight requests to this backend; 0 means unlimited.
	MaxConns int `json:"maxConns"`

	Enabled bool `json:"enabled"`

	// SkipTLSVerify accepts an untrusted backend certificate. Only sensible
	// for internal backends using self-signed certs.
	SkipTLSVerify bool `json:"skipTlsVerify"`
}

// URL is the base address requests are forwarded to.
func (u Upstream) URL() *url.URL {
	return &url.URL{Scheme: u.Scheme, Host: u.Address}
}

// HealthCheck describes how a Host's upstreams are probed. A zero Interval
// disables active probing, leaving upstreams marked healthy until a proxied
// request fails.
type HealthCheck struct {
	Enabled  bool          `json:"enabled"`
	Path     string        `json:"path"`
	Interval time.Duration `json:"interval"`
	Timeout  time.Duration `json:"timeout"`

	// HealthyThreshold consecutive successes are needed to bring a backend
	// back up; UnhealthyThreshold consecutive failures take it out.
	HealthyThreshold   int `json:"healthyThreshold"`
	UnhealthyThreshold int `json:"unhealthyThreshold"`

	// ExpectStatus is the HTTP status a probe must return. 0 accepts any
	// 2xx or 3xx.
	ExpectStatus int `json:"expectStatus"`
}

// PassiveHealth takes a backend out of rotation when real requests to it keep
// failing to connect, without waiting for the next active probe.
//
// Active probing alone leaves a window of up to Interval x UnhealthyThreshold
// in which the proxy keeps sending traffic to a backend it has already seen
// fail — and leaves no protection at all when probing is switched off. This
// closes both gaps using information the request path produces anyway.
//
// Only connection-level failures count: a dial error, a timeout, a refused
// connection. A backend answering 500 is alive and may be answering 500 for a
// perfectly good reason, so its responses are never grounds for ejection.
type PassiveHealth struct {
	Enabled bool `json:"enabled"`
	// MaxFails is how many consecutive failures eject the backend.
	MaxFails int `json:"maxFails"`
	// EjectFor is how long it stays out before traffic is allowed back.
	EjectFor time.Duration `json:"ejectFor"`
}

// DefaultPassiveHealth is applied to new hosts that do not specify one.
func DefaultPassiveHealth() PassiveHealth {
	return PassiveHealth{Enabled: true, MaxFails: 3, EjectFor: 30 * time.Second}
}

// DefaultHealthCheck is applied to new hosts that do not specify one.
func DefaultHealthCheck() HealthCheck {
	return HealthCheck{
		Enabled:            true,
		Path:               "/",
		Interval:           10 * time.Second,
		Timeout:            5 * time.Second,
		HealthyThreshold:   2,
		UnhealthyThreshold: 3,
	}
}

// Normalize fills in defaults and canonicalises user input so that validation
// and storage both see the same shape. It is always safe to call twice.
// NormalizeUpstreams applies the defaults every upstream gets, wherever it is
// configured. Locations carry their own upstreams, so these rules live here
// rather than inside Host.Normalize where only one caller could reach them.
func NormalizeUpstreams(ups []Upstream) {
	for i := range ups {
		u := &ups[i]
		u.Scheme = strings.ToLower(strings.TrimSpace(u.Scheme))
		if u.Scheme == "" {
			u.Scheme = "http"
		}
		u.Address = strings.TrimSpace(u.Address)
		if u.Weight <= 0 {
			u.Weight = 1
		}
		if u.MaxConns < 0 {
			u.MaxConns = 0
		}
	}
}

// ValidateUpstreams checks one set of upstreams, reporting problems under the
// given field prefix so a form can mark the right row whether the upstream
// belongs to a host or to one of its locations.
func ValidateUpstreams(v *ValidationError, prefix string, ups []Upstream) {
	seen := make(map[string]struct{}, len(ups))
	for i, u := range ups {
		field := prefix + "[" + strconv.Itoa(i) + "]"
		if u.Scheme != "http" && u.Scheme != "https" {
			v.Add(field+".scheme", "must be http or https")
		}
		if err := validateHostPort(u.Address); err != nil {
			v.Add(field+".address", "%s", err.Error())
		}
		if u.Weight < 1 || u.Weight > 1000 {
			v.Add(field+".weight", "must be between 1 and 1000")
		}
		key := u.Scheme + "://" + u.Address
		if _, dup := seen[key]; dup {
			v.Add(field+".address", "duplicate upstream %s", key)
		}
		seen[key] = struct{}{}
	}
}

func (h *Host) Normalize() {
	h.Name = strings.TrimSpace(h.Name)
	if h.Algorithm == "" {
		h.Algorithm = RoundRobin
	}

	domains := make([]string, 0, len(h.Domains))
	seen := make(map[string]struct{}, len(h.Domains))
	for _, d := range h.Domains {
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
	h.Domains = domains

	NormalizeUpstreams(h.Upstreams)

	hc := &h.HealthCheck
	if hc.Path == "" {
		hc.Path = "/"
	}
	if !strings.HasPrefix(hc.Path, "/") {
		hc.Path = "/" + hc.Path
	}
	if hc.Interval <= 0 {
		hc.Interval = 10 * time.Second
	}
	if hc.Timeout <= 0 || hc.Timeout > hc.Interval {
		// A probe that outlives its own interval would stack up forever.
		hc.Timeout = min(5*time.Second, hc.Interval)
	}
	if hc.HealthyThreshold <= 0 {
		hc.HealthyThreshold = 2
	}
	if hc.UnhealthyThreshold <= 0 {
		hc.UnhealthyThreshold = 3
	}

	h.Guardian.Normalize()
	h.Cache.Normalize()
	h.TrafficLimits.Normalize()
	h.Maintenance.Normalize()
	h.ErrorPages.Normalize()
	h.UsageAlert.Normalize()
	h.Headers.Normalize()
	h.Compression.Normalize()

	for i := range h.Locations {
		h.Locations[i].Normalize()
		h.Locations[i].Position = i
	}
	// Longest path first, so the request path can take the first match
	// instead of scanning for the best one on every request.
	sort.SliceStable(h.Locations, func(i, j int) bool {
		return len(h.Locations[i].Path) > len(h.Locations[j].Path)
	})

	ph := &h.PassiveHealth
	if ph.MaxFails <= 0 {
		ph.MaxFails = 3
	}
	if ph.EjectFor <= 0 {
		ph.EjectFor = 30 * time.Second
	}
}

// Validate reports every problem with the host at once. Call Normalize first.
func (h *Host) Validate() error {
	v := &ValidationError{}

	if h.Name == "" {
		v.Add("name", "is required")
	} else if len(h.Name) > 100 {
		v.Add("name", "must be at most 100 characters")
	}

	if len(h.Domains) == 0 {
		v.Add("domains", "at least one domain is required")
	}
	for i, d := range h.Domains {
		if err := validateDomain(d); err != nil {
			v.Add("domains["+strconv.Itoa(i)+"]", "%s", err.Error())
		}
	}

	if !h.Algorithm.Valid() {
		v.Add("algorithm", "%q is not a supported algorithm", string(h.Algorithm))
	}

	if len(h.Upstreams) == 0 {
		v.Add("upstreams", "at least one upstream is required")
	}
	ValidateUpstreams(v, "upstreams", h.Upstreams)

	if h.HSTSMaxAge < 0 {
		v.Add("hstsMaxAge", "must not be negative")
	}
	if h.HSTSMaxAge > 0 && h.CertificateID == nil {
		v.Add("hstsMaxAge", "requires a certificate, since HSTS only applies to TLS responses")
	}
	if h.ForceHTTPS && h.CertificateID == nil {
		v.Add("forceHttps", "requires a certificate")
	}

	if h.HealthCheck.ExpectStatus != 0 &&
		(h.HealthCheck.ExpectStatus < 100 || h.HealthCheck.ExpectStatus > 599) {
		v.Add("healthCheck.expectStatus", "must be a valid HTTP status, or 0 to accept any 2xx/3xx")
	}

	if err := h.Guardian.Validate(); err != nil {
		var guard *ValidationError
		if errors.As(err, &guard) {
			v.Fields = append(v.Fields, guard.Fields...)
		}
	}

	if err := h.Cache.Validate(); err != nil {
		var cacheErr *ValidationError
		if errors.As(err, &cacheErr) {
			v.Fields = append(v.Fields, cacheErr.Fields...)
		}
	}

	if err := h.TrafficLimits.Validate(); err != nil {
		var limitErr *ValidationError
		if errors.As(err, &limitErr) {
			v.Fields = append(v.Fields, limitErr.Fields...)
		}
	}

	if err := h.Maintenance.Validate(); err != nil {
		var pageErr *ValidationError
		if errors.As(err, &pageErr) {
			v.Fields = append(v.Fields, pageErr.Fields...)
		}
	}

	if err := h.ErrorPages.Validate(); err != nil {
		var pageErr *ValidationError
		if errors.As(err, &pageErr) {
			v.Fields = append(v.Fields, pageErr.Fields...)
		}
	}

	if err := h.UsageAlert.Validate(); err != nil {
		var usageErr *ValidationError
		if errors.As(err, &usageErr) {
			v.Fields = append(v.Fields, usageErr.Fields...)
		}
	}

	h.Headers.Validate(v, "headers")

	if err := h.Compression.Validate(); err != nil {
		var compErr *ValidationError
		if errors.As(err, &compErr) {
			v.Fields = append(v.Fields, compErr.Fields...)
		}
	}

	if len(h.Locations) > MaxLocationsPerHost {
		v.Add("locations", "must be at most %d", MaxLocationsPerHost)
	}
	seenPath := make(map[string]struct{}, len(h.Locations))
	for i := range h.Locations {
		if err := h.Locations[i].Validate(i); err != nil {
			var locErr *ValidationError
			if errors.As(err, &locErr) {
				v.Fields = append(v.Fields, locErr.Fields...)
			}
		}
		// Two locations on the same path is not a preference between them;
		// it is a configuration with no defined answer.
		if _, dup := seenPath[h.Locations[i].Path]; dup {
			v.Add("locations["+strconv.Itoa(i)+"].path",
				"duplicate path %q", h.Locations[i].Path)
		}
		seenPath[h.Locations[i].Path] = struct{}{}
	}

	if h.PassiveHealth.MaxFails < 1 || h.PassiveHealth.MaxFails > 100 {
		v.Add("passiveHealth.maxFails", "must be between 1 and 100")
	}
	if h.PassiveHealth.EjectFor < time.Second || h.PassiveHealth.EjectFor > time.Hour {
		v.Add("passiveHealth.ejectFor", "must be between 1 second and 1 hour")
	}

	return v.Err()
}

// EnabledUpstreams returns the upstreams an operator has not switched off.
// Health state is tracked separately, at runtime, by the balancer pool.
func (h *Host) EnabledUpstreams() []Upstream {
	out := make([]Upstream, 0, len(h.Upstreams))
	for _, u := range h.Upstreams {
		if u.Enabled {
			out = append(out, u)
		}
	}
	return out
}

// validateDomain accepts hostnames and single-label wildcards ("*.example.com").
func validateDomain(d string) error {
	if d == "" {
		return errString("is empty")
	}
	if len(d) > 253 {
		return errString("is longer than 253 characters")
	}
	labels := strings.Split(d, ".")
	for i, label := range labels {
		if label == "*" {
			if i != 0 {
				return errString("wildcard is only allowed as the leftmost label")
			}
			if len(labels) < 3 {
				return errString("wildcard must cover a registrable domain, e.g. *.example.com")
			}
			continue
		}
		if err := validateLabel(label); err != nil {
			return err
		}
	}
	return nil
}

func validateLabel(label string) error {
	if label == "" {
		return errString("contains an empty label")
	}
	if len(label) > 63 {
		return errString("contains a label longer than 63 characters")
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return errString("has a label starting or ending with a hyphen")
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return errString("contains an invalid character; use a-z, 0-9 and hyphen")
		}
	}
	return nil
}

// validateHostPort requires an explicit port so routing is never ambiguous.
func validateHostPort(addr string) error {
	if addr == "" {
		return errString("is required")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errString("must be host:port, e.g. 10.0.0.5:8080")
	}
	if host == "" {
		return errString("is missing a host")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errString("has an invalid port")
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	return validateDomain(host)
}

// errString is a tiny helper so the validators above read as one-liners.
type errString string

func (e errString) Error() string { return string(e) }
