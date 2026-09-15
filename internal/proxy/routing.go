// Package proxy is the data plane: it accepts client connections, decides
// which host and backend a request belongs to, and forwards it.
package proxy

import (
	"strings"

	"github.com/ponzproxy/ponzproxy/internal/balancer"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// route is one host's configuration paired with its live pool.
//
// Routes are immutable. A configuration change builds a whole new routing
// table and swaps it in, so a request that has already picked its route is
// never affected mid-flight.
type route struct {
	host domain.Host
	pool *balancer.Pool
	// access is resolved when the table is built, so the request path never
	// looks a list up or takes a lock to read one.
	access *domain.AccessList
}

// routingTable resolves a request's Host header to a route.
//
// Exact names and wildcards are kept apart so lookup is a map hit in the
// common case, with a single fallback probe for the wildcard parent. That
// keeps matching O(1) regardless of how many hosts are configured, rather than
// scanning a list of patterns per request.
type routingTable struct {
	exact    map[string]*route
	wildcard map[string]*route // keyed by the parent domain, without "*."
	routes   []*route          // every route, in configuration order

	// Redirects answer a domain themselves and have no pool, so they are
	// resolved separately. The store guarantees a domain is never in both.
	redirectExact    map[string]*domain.Redirect
	redirectWildcard map[string]*domain.Redirect
}

// buildRoutingTable constructs the table for a set of hosts. prev supplies the
// pools whose runtime state should be carried over, so an edit to one host
// does not reset health anywhere.
//
// Disabled hosts are kept out of the lookup maps but stay in routes, so the
// dashboard can still show them.
func buildRoutingTable(cfg Config, prev *routingTable) *routingTable {
	t := &routingTable{
		exact:            make(map[string]*route, len(cfg.Hosts)),
		wildcard:         make(map[string]*route),
		routes:           make([]*route, 0, len(cfg.Hosts)),
		redirectExact:    make(map[string]*domain.Redirect, len(cfg.Redirects)),
		redirectWildcard: make(map[string]*domain.Redirect),
	}

	for _, h := range cfg.Hosts {
		r := &route{host: h, pool: balancer.NewPool(h.ID, h.Algorithm, h.Upstreams, prev.poolFor(h.ID))}
		if h.AccessListID != nil {
			r.access = cfg.AccessLists[*h.AccessListID]
		}
		t.routes = append(t.routes, r)

		if !h.Enabled {
			continue
		}
		for _, d := range h.Domains {
			if parent, ok := strings.CutPrefix(d, "*."); ok {
				t.wildcard[parent] = r
				continue
			}
			t.exact[d] = r
		}
	}

	for i := range cfg.Redirects {
		rd := &cfg.Redirects[i]
		if !rd.Enabled {
			continue
		}
		for _, d := range rd.Domains {
			if parent, ok := strings.CutPrefix(d, "*."); ok {
				t.redirectWildcard[parent] = rd
				continue
			}
			t.redirectExact[d] = rd
		}
	}
	return t
}

// lookupRedirect resolves a Host header to a redirect. Exact beats wildcard,
// as it does for hosts. A domain can never be in both tables: the store
// refuses to save that.
func (t *routingTable) lookupRedirect(hostHeader string) *domain.Redirect {
	name := normalizeHostHeader(hostHeader)
	if name == "" {
		return nil
	}
	if rd, ok := t.redirectExact[name]; ok {
		return rd
	}
	if _, parent, found := strings.Cut(name, "."); found {
		if rd, ok := t.redirectWildcard[parent]; ok {
			return rd
		}
	}
	return nil
}

// poolFor returns the previous pool for a host, or nil when there was none.
// It tolerates a nil receiver so the first build needs no special case.
func (t *routingTable) poolFor(hostID int64) *balancer.Pool {
	if t == nil {
		return nil
	}
	for _, r := range t.routes {
		if r.host.ID == hostID {
			return r.pool
		}
	}
	return nil
}

// lookup resolves a Host header to a route.
//
// An exact match always wins over a wildcard, so "api.example.com" can be
// configured separately from "*.example.com". The wildcard covers exactly one
// label, matching how TLS certificates treat them.
func (t *routingTable) lookup(hostHeader string) *route {
	name := normalizeHostHeader(hostHeader)
	if name == "" {
		return nil
	}
	if r, ok := t.exact[name]; ok {
		return r
	}
	if _, parent, found := strings.Cut(name, "."); found {
		if r, ok := t.wildcard[parent]; ok {
			return r
		}
	}
	return nil
}

// certificateFor reports which certificate should terminate TLS for a server
// name, by resolving it through the same rules as request routing.
func (t *routingTable) certificateFor(serverName string) (*route, bool) {
	r := t.lookup(serverName)
	if r == nil || r.host.CertificateID == nil {
		return nil, false
	}
	return r, true
}

// normalizeHostHeader strips the port, trailing dot and case from a Host
// header so it can be compared against configured domains.
func normalizeHostHeader(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}

	// An IPv6 literal is bracketed, so the last colon only separates a port
	// when it comes after the closing bracket.
	if strings.HasPrefix(h, "[") {
		if end := strings.IndexByte(h, ']'); end >= 0 {
			h = h[1:end]
		}
	} else if idx := strings.LastIndexByte(h, ':'); idx >= 0 {
		// Only treat it as a port if what follows is actually numeric;
		// a bare IPv6 address without brackets has several colons.
		if port := h[idx+1:]; port != "" && isAllDigits(port) {
			h = h[:idx]
		}
	}

	return strings.ToLower(strings.TrimSuffix(h, "."))
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
