package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/alerts"
	"github.com/ponzproxy/ponzproxy/internal/balancer"
	"github.com/ponzproxy/ponzproxy/internal/metrics"
)

var errNoCertificate = errors.New("no certificate configured for this server name")

// requestState carries per-request routing decisions between ServeHTTP, the
// rewrite hook and the error handler, which httputil.ReverseProxy only
// connects through the request context.
type requestState struct {
	route   *route
	backend *balancer.Backend
	// upstreamErr is set by the error handler when the backend could not be
	// reached, which is what tells the retry loop to try another one.
	upstreamErr error
}

type stateKey struct{}

// ServeHTTP is the entry point for every proxied request.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// ACME HTTP-01 challenges must be answered on :80 for domains that are
	// not routed yet — that is the whole point of the challenge — so this
	// comes before routing.
	if r.TLS == nil && e.certResolve != nil && e.certResolve.HandleACMEChallenge(w, r) {
		return
	}

	table := e.table.Load()

	// A redirect answers the domain itself; there is no route and no pool,
	// so this comes before host lookup. A domain cannot be both — the store
	// refuses to save that.
	if rd := table.lookupRedirect(r.Host); rd != nil {
		http.Redirect(w, r, rd.Location(r.URL), rd.StatusCode)
		e.record(r, nil, start, rd.StatusCode, 0, false)
		return
	}

	rt := table.lookup(r.Host)
	if rt == nil {
		e.rejectUnmatched(w, r, start)
		return
	}

	if e.redirectToHTTPS(w, r, rt) {
		return
	}

	// Before the traffic limits on purpose: a maintenance page reaches no
	// backend and costs a few hundred bytes, so refusing it with 429 would
	// tell a visitor "too many requests" when the truth is "planned work".
	if e.serveMaintenance(w, r, rt, start) {
		return
	}

	// Before the access list on purpose: basic auth compares a bcrypt hash,
	// so a client inventing credentials could otherwise buy a thousand
	// bcrypt comparisons a second from one connection.
	allowed, release := e.enforceLimits(w, r, rt, start)
	defer release()
	if !allowed {
		return
	}

	// After the HTTPS redirect on purpose: a host that forces TLS must never
	// have basic auth credentials prompted for, or sent, in the clear.
	if !e.enforceAccess(w, r, rt, start) {
		return
	}

	if !e.inspectRequest(w, r, rt, start) {
		return
	}

	if isWebSocketUpgrade(r) && !rt.host.WebSocketSupport {
		e.finish(w, r, rt, start, http.StatusForbidden,
			"WebSocket upgrades are not enabled for this host")
		return
	}

	// After access control and inspection on purpose: a request those would
	// have refused must never be answered from cache instead.
	if w = e.interceptCache(w, r, rt, &rt.host.Cache, start); w == nil {
		return
	}

	e.forward(w, r, rt, start)
}

// forward runs the pick-and-proxy loop, moving to another backend when one
// cannot be reached.
func (e *Engine) forward(w http.ResponseWriter, r *http.Request, rt *route, start time.Time) {
	collector := e.opts.Collector
	if collector != nil {
		collector.RequestStarted(rt.host.ID)
		defer collector.RequestFinished(rt.host.ID)
	}

	clientIP := e.clientIP(r)
	rec := &recorder{ResponseWriter: w}

	// Set before forwarding: ReverseProxy adds the upstream's headers to
	// this map rather than replacing it, so the value survives, and a
	// backend that sets its own HSTS is left alone.
	if r.TLS != nil && rt.host.HSTSMaxAge > 0 && rec.Header().Get("Strict-Transport-Security") == "" {
		rec.Header().Set("Strict-Transport-Security",
			"max-age="+strconv.Itoa(rt.host.HSTSMaxAge))
	}

	var (
		tried    []*balancer.Backend
		lastFail *balancer.Backend
		lastErr  error
	)
	for attempt := 0; ; attempt++ {
		backend, err := rt.pool.Pick(clientIP, tried)
		if err != nil {
			// Running out of candidates after real connection failures is
			// a different condition from having none to begin with: the
			// upstreams exist, they just could not be reached.
			if lastErr != nil {
				e.serveBadGateway(rec, r, rt, start, lastFail, lastErr)
				return
			}
			e.serveUnavailable(rec, r, rt, start, err, len(tried))
			return
		}
		tried = append(tried, backend)

		state := &requestState{route: rt, backend: backend}
		attemptStart := time.Now()

		backend.Acquire()
		e.reverseProxy.ServeHTTP(rec, r.WithContext(
			context.WithValue(r.Context(), stateKey{}, state)))
		backend.Release(time.Since(attemptStart))

		if state.upstreamErr == nil {
			backend.RecordError(nil)
			// A success clears any passive ejection: the backend is
			// evidently answering again.
			backend.RecordProxyResult(true, rt.host.PassiveHealth, time.Now())
			e.record(r, rt, start, rec.status, rec.written, false)
			e.logAccess(r, rt, backend, start, rec.status, rec.written, nil)
			e.recordAccess(r, rt, backend, start, rec.status, rec.written, nil)
			return
		}

		backend.RecordError(state.upstreamErr)
		lastFail, lastErr = backend, state.upstreamErr

		// Passive health: the request path just proved this backend could
		// not be reached, so feed that back rather than waiting for the
		// next active probe — or forever, when probing is switched off.
		if ejected := backend.RecordProxyResult(false, rt.host.PassiveHealth, time.Now()); ejected {
			_, remaining := backend.Ejected(time.Now())
			e.logger.Warn("upstream ejected after repeated connection failures",
				"host", rt.host.Name, "upstream", backend.Key(),
				"failures", rt.host.PassiveHealth.MaxFails,
				"ejectedFor", remaining.Round(time.Second),
				"error", state.upstreamErr)
			alerts.UpstreamEjected(e.opts.Alerts, rt.host.Name, backend.Key(),
				remaining, state.upstreamErr.Error())
		}

		// A retry is only sound while the client has seen nothing and the
		// request body has not been consumed. Replaying a request whose
		// body was already streamed upstream would send a truncated copy.
		if !rec.dirty() && canReplay(r) && attempt < e.opts.MaxRetries {
			e.logger.Debug("retrying on another upstream",
				"host", rt.host.Name, "upstream", backend.Key(), "error", state.upstreamErr)
			continue
		}

		e.serveBadGateway(rec, r, rt, start, backend, state.upstreamErr)
		return
	}
}

// buildReverseProxy wires the shared reverse proxy. One instance serves every
// route; the per-request decisions travel through the context.
func (e *Engine) buildReverseProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite:      e.rewrite,
		Transport:    roundTripperFunc(e.roundTrip),
		ErrorHandler: e.handleUpstreamError,
		ErrorLog:     slogErrorLog(e.logger),
	}
}

// rewrite turns the inbound request into the one sent upstream.
func (e *Engine) rewrite(pr *httputil.ProxyRequest) {
	state := stateFrom(pr.In.Context())
	if state == nil {
		return // unreachable: forward always installs the state
	}

	pr.Out.URL.Scheme = state.backend.URL.Scheme
	pr.Out.URL.Host = state.backend.URL.Host

	// SetXForwarded appends the client to X-Forwarded-For and sets Proto
	// and Host, replacing any values the client supplied so they cannot be
	// spoofed.
	pr.SetXForwarded()

	if state.route.host.PreserveHost {
		pr.Out.Host = pr.In.Host
	} else {
		pr.Out.Host = state.backend.URL.Host
	}
}

// roundTrip sends the request using the transport matching the backend's TLS
// policy.
func (e *Engine) roundTrip(r *http.Request) (*http.Response, error) {
	state := stateFrom(r.Context())
	if state == nil {
		return nil, errors.New("proxy: request reached the transport without routing state")
	}
	return e.transportFor(state.backend.Upstream.SkipTLSVerify).RoundTrip(r)
}

// handleUpstreamError records the failure instead of writing a response, so
// the retry loop still owns the decision about what the client sees.
func (e *Engine) handleUpstreamError(_ http.ResponseWriter, r *http.Request, err error) {
	if state := stateFrom(r.Context()); state != nil {
		state.upstreamErr = err
	}
}

func stateFrom(ctx context.Context) *requestState {
	state, _ := ctx.Value(stateKey{}).(*requestState)
	return state
}

// redirectToHTTPS sends a plaintext request to the TLS listener when the host
// requires it. It reports whether the request was handled.
func (e *Engine) redirectToHTTPS(w http.ResponseWriter, r *http.Request, rt *route) bool {
	if r.TLS != nil || !rt.host.ForceHTTPS {
		return false
	}

	target := *r.URL
	target.Scheme = "https"
	target.Host = redirectHost(r.Host, e.httpsPort)

	// 308 preserves the method and body, unlike 301, so a POST is not
	// silently turned into a GET.
	http.Redirect(w, r, target.String(), http.StatusPermanentRedirect)
	return true
}

// redirectHost rewrites the authority for an https redirect, dropping the
// plaintext port and adding the TLS one only when it is non-standard.
func redirectHost(hostHeader, httpsPort string) string {
	name := normalizeHostHeader(hostHeader)
	if httpsPort == "" || httpsPort == "443" {
		return name
	}
	return net.JoinHostPort(name, httpsPort)
}

func (e *Engine) rejectUnmatched(w http.ResponseWriter, r *http.Request, start time.Time) {
	const body = "No host is configured for this domain."
	writeProblem(w, http.StatusNotFound, body)
	e.record(r, nil, start, http.StatusNotFound, int64(len(body)), false)
	e.logger.Debug("no route for request",
		"host", r.Host, "path", r.URL.Path, "remote", r.RemoteAddr)
}

func (e *Engine) serveUnavailable(rec *recorder, r *http.Request, rt *route,
	start time.Time, cause error, tried int) {

	msg := "No upstream is available to serve this request."
	if errors.Is(cause, balancer.ErrNoBackends) {
		msg = "This host has no upstream configured."
	}
	title, body := errorPage(&rt.host.ErrorPages, http.StatusServiceUnavailable, msg)
	writePage(rec, http.StatusServiceUnavailable, title, body, 0)

	e.record(r, rt, start, http.StatusServiceUnavailable, rec.written, true)
	e.logger.Warn("no upstream available",
		"host", rt.host.Name, "error", cause, "attempts", tried)
	e.logAccess(r, rt, nil, start, http.StatusServiceUnavailable, rec.written, cause)
	e.recordAccess(r, rt, nil, start, http.StatusServiceUnavailable, rec.written, cause)
}

func (e *Engine) serveBadGateway(rec *recorder, r *http.Request, rt *route,
	start time.Time, backend *balancer.Backend, cause error) {

	if rec.dirty() {
		// The client already has part of a response; the connection is the
		// only signal left, so let the server tear it down.
		e.record(r, rt, start, rec.status, rec.written, true)
		e.logAccess(r, rt, backend, start, rec.status, rec.written, cause)
		e.recordAccess(r, rt, backend, start, rec.status, rec.written, cause)
		return
	}

	title, body := errorPage(&rt.host.ErrorPages, http.StatusBadGateway,
		"The upstream server could not be reached.")
	writePage(rec, http.StatusBadGateway, title, body, 0)
	e.record(r, rt, start, http.StatusBadGateway, rec.written, true)
	e.logger.Warn("upstream request failed",
		"host", rt.host.Name, "upstream", backend.Key(), "error", cause)
	e.logAccess(r, rt, backend, start, http.StatusBadGateway, rec.written, cause)
	e.recordAccess(r, rt, backend, start, http.StatusBadGateway, rec.written, cause)
}

// finish writes a plain refusal that never reached an upstream.
func (e *Engine) finish(w http.ResponseWriter, r *http.Request, rt *route,
	start time.Time, status int, msg string) {

	writeProblem(w, status, msg)
	e.record(r, rt, start, status, int64(len(msg)), false)
	e.recordAccess(r, rt, nil, start, status, int64(len(msg)), nil)
}

func (e *Engine) record(r *http.Request, rt *route, start time.Time,
	status int, bytesOut int64, failed bool) {

	if e.opts.Collector == nil {
		return
	}
	hostID := metrics.UnmatchedHostID
	if rt != nil {
		hostID = rt.host.ID
	}
	e.opts.Collector.Record(metrics.Result{
		HostID:   hostID,
		Status:   status,
		BytesIn:  requestBytes(r),
		BytesOut: bytesOut,
		Duration: time.Since(start),
		Failed:   failed,
	})
}

// writeProblem sends a short plain-text error. Upstream headers are never
// involved, so nothing from a failed backend leaks to the client.
func writeProblem(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(msg)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

// canReplay reports whether a request may be sent to a second backend.
//
// Only bodyless requests qualify. Go gives a server handler the body as a
// one-shot stream with no way to rewind it, so any request carrying one would
// arrive truncated on the retry — a far worse outcome than the 502 the client
// gets instead.
func canReplay(r *http.Request) bool {
	return r.ContentLength == 0 && (r.Body == nil || r.Body == http.NoBody)
}

// requestBytes estimates the inbound size. A chunked request reports -1, in
// which case only the headers are counted rather than guessing.
func requestBytes(r *http.Request) int64 {
	n := int64(len(r.Method) + len(r.URL.String()) + len(r.Proto) + 4)
	for k, vs := range r.Header {
		for _, v := range vs {
			n += int64(len(k) + len(v) + 4)
		}
	}
	if r.ContentLength > 0 {
		n += r.ContentLength
	}
	return n
}

// isWebSocketUpgrade detects an Upgrade handshake without pulling in
// golang.org/x/net just to read two headers.
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		headerHasToken(r.Header, "Connection", "upgrade")
}

func headerHasToken(h http.Header, key, token string) bool {
	for _, v := range h.Values(key) {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
