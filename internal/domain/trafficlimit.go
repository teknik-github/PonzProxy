package domain

import (
	"net"
	"strings"
)

// TrafficLimits bounds what one client may ask of a host.
//
// It is not DDoS protection and must not be described as such: a volumetric
// attack saturates the uplink long before a packet reaches this process. What
// it does cover is the thing that actually takes small deployments down — one
// client, or a handful, asking for more than the backends can serve. That is
// abuse the proxy is in exactly the right place to refuse, because refusing it
// here costs a few hundred nanoseconds instead of a database query.
type TrafficLimits struct {
	Mode Mode `json:"mode"`
	// RequestsPerSecond is the sustained rate allowed per client address.
	// Zero leaves the rate unlimited while other bounds still apply.
	RequestsPerSecond int `json:"requestsPerSecond"`
	// Burst is how far above that rate a client may go momentarily. A page
	// load is a burst of a dozen requests; a limit without headroom would
	// refuse ordinary browsing.
	Burst int `json:"burst"`
	// MaxConcurrent caps requests in flight from one address. The rate
	// limit does not bound this on its own: a client can stay under the
	// rate while holding every connection open on slow endpoints.
	MaxConcurrent int `json:"maxConcurrent"`
	// MaxBodyBytes refuses request bodies larger than this. Zero allows
	// any size.
	MaxBodyBytes int64 `json:"maxBodyBytes"`
	// Exempt lists CIDRs the limits never apply to: monitoring probes,
	// an office address, a health checker. Without it, switching limits on
	// silently starts refusing your own uptime checks.
	Exempt []string `json:"exempt"`

	// exempt is Exempt parsed once at load, so the request path does no
	// parsing. It is unexported and rebuilt by Normalize.
	exempt []*net.IPNet
}

// Limit bounds. The maxima are sanity rails rather than opinions: a value past
// them is far more likely to be a typo than a plan.
const (
	MaxRequestsPerSecond = 1_000_000
	MaxBurstRequests     = 1_000_000
	MaxConcurrentPerIP   = 100_000
	MaxRequestBodyBytes  = 1 << 40 // 1 TiB
	MaxExemptEntries     = 64
)

// DefaultTrafficLimits is applied to new hosts. It is off, for the same reason
// the guardian is: turning a refusal on for someone's existing traffic without
// being asked is how a proxy earns a reputation for breaking things. The
// numbers are what an operator gets when they switch it on, chosen to sit well
// above ordinary browsing.
func DefaultTrafficLimits() TrafficLimits {
	return TrafficLimits{
		Mode:              ModeOff,
		RequestsPerSecond: 50,
		Burst:             100,
		MaxConcurrent:     40,
		MaxBodyBytes:      32 << 20, // 32 MiB
		Exempt:            []string{},
	}
}

// Enabled reports whether any limit is checked at all.
func (t *TrafficLimits) Enabled() bool { return t.Mode.Enabled() }

// ExemptNets returns the parsed exemptions. Normalize must have run.
func (t *TrafficLimits) ExemptNets() []*net.IPNet { return t.exempt }

// IsExempt reports whether an address is outside the limits entirely.
func (t *TrafficLimits) IsExempt(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range t.exempt {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Normalize fills in defaults, canonicalises operator input and parses the
// exemptions once.
func (t *TrafficLimits) Normalize() {
	t.Mode = NormalizeMode(t.Mode)

	if t.RequestsPerSecond < 0 {
		t.RequestsPerSecond = 0
	}
	if t.MaxConcurrent < 0 {
		t.MaxConcurrent = 0
	}
	if t.MaxBodyBytes < 0 {
		t.MaxBodyBytes = 0
	}
	// A burst below the rate would refuse traffic the rate allows, which
	// reads as the limiter being broken rather than strict.
	if t.Burst < t.RequestsPerSecond {
		t.Burst = t.RequestsPerSecond
	}

	seen := make(map[string]struct{}, len(t.Exempt))
	cidrs := make([]string, 0, len(t.Exempt))
	nets := make([]*net.IPNet, 0, len(t.Exempt))
	for _, raw := range t.Exempt {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		// A bare address is the common way to write a single host, and
		// rejecting it would be pedantry.
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
	t.Exempt = cidrs
	t.exempt = nets
}

// Validate reports every problem at once, so a form shows them together.
func (t *TrafficLimits) Validate() error {
	v := &ValidationError{}

	if !t.Mode.Valid() {
		v.Add("trafficLimits.mode", "must be off, detect or block")
	}
	if t.RequestsPerSecond > MaxRequestsPerSecond {
		v.Add("trafficLimits.requestsPerSecond", "must be at most %d", MaxRequestsPerSecond)
	}
	if t.Burst > MaxBurstRequests {
		v.Add("trafficLimits.burst", "must be at most %d", MaxBurstRequests)
	}
	if t.MaxConcurrent > MaxConcurrentPerIP {
		v.Add("trafficLimits.maxConcurrent", "must be at most %d", MaxConcurrentPerIP)
	}
	if t.MaxBodyBytes > MaxRequestBodyBytes {
		v.Add("trafficLimits.maxBodyBytes", "must be at most %d bytes", MaxRequestBodyBytes)
	}
	if len(t.Exempt) > MaxExemptEntries {
		v.Add("trafficLimits.exempt", "must be at most %d entries", MaxExemptEntries)
	}
	for _, entry := range t.Exempt {
		if _, _, err := net.ParseCIDR(entry); err != nil {
			v.Add("trafficLimits.exempt", "%q is not an address or CIDR range", entry)
		}
	}
	// Enforcing nothing is a setting that looks switched on and does
	// nothing, which is worse than being off.
	if t.Enabled() && t.RequestsPerSecond == 0 && t.MaxConcurrent == 0 && t.MaxBodyBytes == 0 {
		v.Add("trafficLimits.mode",
			"set a rate, a concurrency cap or a body size before turning limits on")
	}
	return v.Err()
}
