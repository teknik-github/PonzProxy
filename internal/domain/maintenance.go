package domain

import (
	"net"
	"strings"
)

// Maintenance answers a host with a page instead of proxying it.
//
// The alternative is what people do without it: stop the backend and let
// visitors collect a 502. That is worse in every way — it tells them the site
// is broken rather than being worked on, it is indistinguishable from a real
// outage in your own monitoring, and search engines treat it as one.
type Maintenance struct {
	Enabled bool `json:"enabled"`
	// StatusCode is what the page is served with. 503 is the only honest
	// choice by default: it is the one status that means "temporarily
	// unavailable, come back", and crawlers understand it as such.
	StatusCode int    `json:"statusCode"`
	Title      string `json:"title"`
	Message    string `json:"message"`
	// RetryAfterSeconds fills the Retry-After header. Zero omits it.
	RetryAfterSeconds int `json:"retryAfterSeconds"`
	// AllowFrom lists addresses that reach the backend anyway. Without it,
	// maintenance mode locks out the person performing the maintenance,
	// who then cannot check that the work succeeded.
	AllowFrom []string `json:"allowFrom"`

	allowFrom []*net.IPNet
}

// ErrorPages replaces what a visitor sees when a backend cannot be reached.
//
// Separate from Maintenance because the two are different situations, and
// saying "we are doing planned work" during an unplanned outage is a lie a
// customer will remember.
type ErrorPages struct {
	Enabled bool   `json:"enabled"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

// Limits on the text, which is rendered into a page and must not become a
// place to paste a document.
const (
	MaxPageTitleLength   = 120
	MaxPageMessageLength = 2000
	MaxAllowFromEntries  = 32
	MaxRetryAfterSeconds = 86_400 // a day; past this, use a redirect
)

// DefaultMaintenance is applied to new hosts: off, with wording ready to use.
func DefaultMaintenance() Maintenance {
	return Maintenance{
		Enabled:           false,
		StatusCode:        503,
		Title:             "Down for maintenance",
		Message:           "We are making some changes and will be back shortly. Thank you for your patience.",
		RetryAfterSeconds: 300,
		AllowFrom:         []string{},
	}
}

// DefaultErrorPages is applied to new hosts: off, falling back to the built-in
// wording until an operator writes their own.
func DefaultErrorPages() ErrorPages {
	return ErrorPages{
		Enabled: false,
		Title:   "This site is temporarily unavailable",
		Message: "Something went wrong on our side. Please try again in a few moments.",
	}
}

// Allows reports whether an address bypasses maintenance.
func (m *Maintenance) Allows(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range m.allowFrom {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Normalize fills in defaults and parses the bypass list once, so the request
// path never parses a CIDR.
func (m *Maintenance) Normalize() {
	m.Title = strings.TrimSpace(m.Title)
	m.Message = strings.TrimSpace(m.Message)

	if m.StatusCode == 0 {
		m.StatusCode = 503
	}
	if m.RetryAfterSeconds < 0 {
		m.RetryAfterSeconds = 0
	}
	m.AllowFrom, m.allowFrom = parseCIDRList(m.AllowFrom)
}

// Validate reports every problem at once, so a form shows them together.
func (m *Maintenance) Validate() error {
	v := &ValidationError{}

	// Only the statuses that mean "not now". A maintenance page served with
	// 200 tells every crawler the page it is holding is the real content.
	switch m.StatusCode {
	case 200, 307, 503:
	default:
		v.Add("maintenance.statusCode", "must be 503, 307 or 200")
	}
	if len(m.Title) > MaxPageTitleLength {
		v.Add("maintenance.title", "must be at most %d characters", MaxPageTitleLength)
	}
	if len(m.Message) > MaxPageMessageLength {
		v.Add("maintenance.message", "must be at most %d characters", MaxPageMessageLength)
	}
	if m.RetryAfterSeconds > MaxRetryAfterSeconds {
		v.Add("maintenance.retryAfterSeconds", "must be at most %d", MaxRetryAfterSeconds)
	}
	if len(m.AllowFrom) > MaxAllowFromEntries {
		v.Add("maintenance.allowFrom", "must be at most %d entries", MaxAllowFromEntries)
	}
	for _, entry := range m.AllowFrom {
		if _, _, err := net.ParseCIDR(entry); err != nil {
			v.Add("maintenance.allowFrom", "%q is not an address or CIDR range", entry)
		}
	}
	if m.Enabled && m.Title == "" {
		v.Add("maintenance.title", "give the page a heading before switching it on")
	}
	return v.Err()
}

// Normalize trims the wording.
func (e *ErrorPages) Normalize() {
	e.Title = strings.TrimSpace(e.Title)
	e.Message = strings.TrimSpace(e.Message)
}

func (e *ErrorPages) Validate() error {
	v := &ValidationError{}
	if len(e.Title) > MaxPageTitleLength {
		v.Add("errorPages.title", "must be at most %d characters", MaxPageTitleLength)
	}
	if len(e.Message) > MaxPageMessageLength {
		v.Add("errorPages.message", "must be at most %d characters", MaxPageMessageLength)
	}
	if e.Enabled && e.Title == "" {
		v.Add("errorPages.title", "give the page a heading before switching it on")
	}
	return v.Err()
}

// parseCIDRList canonicalises a list of addresses and ranges, returning the
// cleaned text and the parsed form. A bare address is the common way to write
// a single host, so it is accepted and widened rather than rejected.
func parseCIDRList(in []string) ([]string, []*net.IPNet) {
	seen := make(map[string]struct{}, len(in))
	cidrs := make([]string, 0, len(in))
	nets := make([]*net.IPNet, 0, len(in))

	for _, raw := range in {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			if ip := net.ParseIP(entry); ip != nil {
				if ip.To4() != nil {
					entry += "/32"
				} else {
					entry += "/128"
				}
			}
		}
		if _, dup := seen[entry]; dup {
			continue
		}
		seen[entry] = struct{}{}
		cidrs = append(cidrs, entry)
		if _, n, err := net.ParseCIDR(entry); err == nil {
			nets = append(nets, n)
		}
	}
	return cidrs, nets
}
