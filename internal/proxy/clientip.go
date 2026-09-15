package proxy

import (
	"net"
	"net/http"
	"strings"
)

// clientIP resolves the address used for IP-hash routing and access logs.
//
// By default this is the peer address of the connection, which cannot be
// forged. A header is consulted only when the operator has explicitly named
// one in the configuration, because any client can set X-Forwarded-For at
// will: trusting it unconditionally would let a caller choose its own backend
// under IP hash, and would poison every log line.
func (e *Engine) clientIP(r *http.Request) string {
	if header := e.opts.TrustedClientIPHeader; header != "" {
		if ip := firstForwardedIP(r.Header.Get(header)); ip != "" {
			return ip
		}
	}
	return remoteIP(r.RemoteAddr)
}

// firstForwardedIP takes the left-most entry of an X-Forwarded-For style list,
// which is the original client as recorded by the first proxy in the chain.
func firstForwardedIP(value string) string {
	head, _, _ := strings.Cut(value, ",")
	head = strings.TrimSpace(head)
	if head == "" {
		return ""
	}
	// A port is legal in Forwarded-style headers and must be stripped, but
	// a bare IPv6 address also contains colons.
	if host, _, err := net.SplitHostPort(head); err == nil {
		head = host
	}
	if net.ParseIP(head) == nil {
		return "" // malformed; fall back to the connection address
	}
	return head
}

// remoteIP strips the port from a connection address.
func remoteIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
