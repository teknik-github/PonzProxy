package alerts

import (
	"fmt"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// The constructors below are the only place an alert's wording is decided, so
// the same event always reads the same way whichever part of the proxy noticed
// it. Each one names what happened and what it means for visitors — an alert
// that only says "upstream_down" makes the reader go and look anyway.

// Raiser is the narrow interface the rest of the proxy depends on, so nothing
// outside this package needs the dispatcher itself.
type Raiser interface {
	Raise(domain.Alert)
}

// UpstreamDown reports a backend the active probe has taken out.
func UpstreamDown(r Raiser, host, upstream, reason string) {
	if r == nil {
		return
	}
	a := domain.NewAlert(domain.AlertUpstreamDown,
		host+"/"+upstream,
		fmt.Sprintf("%s: upstream %s is down", host, upstream),
		reason)
	a.Host, a.Upstream = host, upstream
	r.Raise(a)
}

// UpstreamRecovered reports one answering again, so an operator can close an
// incident without going to look.
func UpstreamRecovered(r Raiser, host, upstream string) {
	if r == nil {
		return
	}
	a := domain.NewAlert(domain.AlertUpstreamRecovered,
		host+"/"+upstream,
		fmt.Sprintf("%s: upstream %s is back", host, upstream),
		"It answered a health check again and is taking traffic.")
	a.Host, a.Upstream = host, upstream
	r.Raise(a)
}

// UpstreamEjected reports passive health removing a backend after real
// requests failed to reach it.
func UpstreamEjected(r Raiser, host, upstream string, ejectedFor time.Duration, reason string) {
	if r == nil {
		return
	}
	a := domain.NewAlert(domain.AlertUpstreamEjected,
		host+"/"+upstream,
		fmt.Sprintf("%s: upstream %s ejected for %s", host, upstream, ejectedFor.Round(time.Second)),
		"Real requests could not connect to it: "+reason)
	a.Host, a.Upstream = host, upstream
	r.Raise(a)
}

// HostUnavailable reports a host with nothing left to serve it. This is the
// one that means visitors are seeing errors right now.
func HostUnavailable(r Raiser, host string, total int) {
	if r == nil {
		return
	}
	a := domain.NewAlert(domain.AlertHostUnavailable,
		host,
		fmt.Sprintf("%s has no healthy upstream", host),
		fmt.Sprintf("All %d upstreams are out. Visitors are getting errors now.", total))
	a.Host = host
	r.Raise(a)
}

// CertificateExpiring warns while there is still time to act.
func CertificateExpiring(r Raiser, name string, days int) {
	if r == nil {
		return
	}
	r.Raise(domain.NewAlert(domain.AlertCertificateExpiring,
		"certificate/"+name,
		fmt.Sprintf("Certificate %q expires in %d days", name, days),
		"Automatic renewal has not produced a new one yet."))
}

// CertificateFailed reports a renewal that did not work, which is the only
// warning before a certificate simply expires.
func CertificateFailed(r Raiser, name, reason string) {
	if r == nil {
		return
	}
	r.Raise(domain.NewAlert(domain.AlertCertificateFailed,
		"certificate/"+name,
		fmt.Sprintf("Certificate %q failed to renew", name),
		reason))
}
