package proxy

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/limiter"
)

// enforceLimits applies a host's traffic limits to one request.
//
// It returns whether the request may continue, and a release for the
// concurrency slot it took. The release is always non-nil, so the caller can
// defer it without a nil check — including on the paths where nothing was
// taken.
//
// It runs before the access list on purpose. Basic auth compares a bcrypt
// hash, and while a cache keeps that off the path for repeat visitors, the
// first request from any address pays for it. A client sending a thousand
// invented credentials a second would otherwise buy a thousand bcrypt
// comparisons a second, which is a denial of service with extra steps.
func (e *Engine) enforceLimits(w http.ResponseWriter, r *http.Request, rt *route, start time.Time) (bool, limiter.Release) {
	noop := func() {}
	limits := &rt.host.TrafficLimits
	if !limits.Enabled() {
		return true, noop
	}

	ip := e.clientAddr(r)
	if limits.IsExempt(ip) {
		return true, noop
	}

	// The body check comes first because it needs no bookkeeping: an
	// oversized upload is refused on its declared length alone.
	if v := checkBody(r, limits.MaxBodyBytes); v.Limited {
		return e.applyLimit(w, r, rt, start, v, limits.Mode), noop
	}

	verdict, release := e.limiter.Acquire(rt.host.ID, ip, limiter.Rules{
		RequestsPerSecond: limits.RequestsPerSecond,
		Burst:             limits.Burst,
		MaxConcurrent:     limits.MaxConcurrent,
	})
	if !verdict.Limited {
		// A body of unknown length — chunked, or a lying Content-Length —
		// is capped as it is read instead. MaxBytesReader closes the
		// connection when the cap is passed, which is the only honest
		// answer once a body is already in flight.
		if limits.MaxBodyBytes > 0 && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limits.MaxBodyBytes)
		}
		return true, release
	}

	return e.applyLimit(w, r, rt, start, verdict, limits.Mode), release
}

// checkBody refuses a request whose declared length is already over the limit,
// without reading a byte of it.
func checkBody(r *http.Request, max int64) limiter.Verdict {
	if max <= 0 || r.ContentLength <= max {
		return limiter.Verdict{}
	}
	return limiter.Verdict{Limited: true, Reason: limiter.ReasonBodySize}
}

// applyLimit records a verdict and, in block mode, answers the client. It
// reports whether the request may continue.
func (e *Engine) applyLimit(w http.ResponseWriter, r *http.Request, rt *route,
	start time.Time, v limiter.Verdict, mode domain.Mode) bool {

	blocked := mode == domain.ModeBlock

	level := slog.LevelWarn
	if !blocked {
		// In detect mode this is an observation the operator asked for,
		// not an incident, so it must not read like one in the log.
		level = slog.LevelInfo
	}
	e.logger.Log(r.Context(), level, "request exceeded a traffic limit",
		"host", rt.host.Name, "limit", string(v.Reason),
		"path", r.URL.Path, "client", e.clientIP(r), "blocked", blocked)

	if c := e.opts.Collector; c != nil {
		c.RecordLimited(rt.host.ID, blocked)
	}

	if !blocked {
		return true
	}

	if v.RetryAfter > 0 {
		secs := int(v.RetryAfter / time.Second)
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
	}

	status := http.StatusTooManyRequests
	message := "Too many requests. Please slow down and try again."
	if v.Reason == limiter.ReasonBodySize {
		// A large upload is not the client going too fast, and telling it
		// to retry later would just waste the bandwidth again.
		status = http.StatusRequestEntityTooLarge
		message = "The request body is larger than this server accepts."
		w.Header().Del("Retry-After")
	}
	e.finish(w, r, rt, start, status, message)
	return false
}
