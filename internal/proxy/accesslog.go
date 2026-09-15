package proxy

import (
	"log"
	"log/slog"
	"net/http"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/balancer"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// logAccess emits one structured line per proxied request to the process log.
//
// Level is chosen by outcome rather than logging everything at info: a busy
// proxy would otherwise drown its own operational messages, while failures
// stay visible at the default level.
func (e *Engine) logAccess(r *http.Request, rt *route, backend *balancer.Backend,
	start time.Time, status int, bytesOut int64, cause error) {

	if !e.logger.Enabled(r.Context(), slog.LevelDebug) && status < 400 && cause == nil {
		// Successful requests are debug-level, so skip building the
		// attributes entirely when debug is off.
		return
	}

	attrs := []any{
		"host", rt.host.Name,
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"bytes", bytesOut,
		"duration", time.Since(start).Round(time.Microsecond),
		"client", e.clientIP(r),
	}
	if backend != nil {
		attrs = append(attrs, "upstream", backend.Key())
	}
	if cause != nil {
		attrs = append(attrs, "error", cause)
	}

	switch {
	case cause != nil || status >= 500:
		e.logger.Error("request failed", attrs...)
	case status >= 400:
		e.logger.Info("request rejected", attrs...)
	default:
		e.logger.Debug("request", attrs...)
	}
}

// recordAccess files one finished request in the searchable log, when the host
// asked for it. This is separate from logAccess: that one writes a line for an
// operator watching a terminal, this one stores a row to search later.
//
// The path is recorded without its query string unless the host opts in.
// Query strings routinely carry session tokens, API keys and password-reset
// links, and a searchable store behind a web console is a different exposure
// from a root-only file.
func (e *Engine) recordAccess(r *http.Request, rt *route, backend *balancer.Backend,
	start time.Time, status int, bytesOut int64, cause error) {

	if e.opts.AccessLog == nil || rt == nil || !rt.host.AccessLog.Enabled {
		return
	}

	path := r.URL.Path
	if rt.host.AccessLog.IncludeQuery && r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}

	entry := domain.AccessLogEntry{
		Timestamp:  start,
		HostID:     rt.host.ID,
		Method:     r.Method,
		Path:       path,
		Status:     status,
		DurationMS: time.Since(start).Milliseconds(),
		BytesOut:   bytesOut,
		ClientIP:   e.clientIP(r),
		UserAgent:  r.UserAgent(),
	}
	if backend != nil {
		entry.Upstream = backend.Key()
	}
	if cause != nil {
		entry.Error = cause.Error()
	}

	e.opts.AccessLog.Record(entry)
}

// slogErrorLog adapts slog to the *log.Logger that httputil.ReverseProxy
// writes its internal errors to, so they land in the same structured stream
// instead of on stderr.
func slogErrorLog(logger *slog.Logger) *log.Logger {
	return slog.NewLogLogger(logger.Handler(), slog.LevelDebug)
}
