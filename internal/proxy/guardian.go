package proxy

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/guardian"
)

// inspectRequest applies the host's request inspection. It reports whether the
// request may continue.
//
// It runs after the access list on purpose: a request the address rules were
// going to refuse anyway should not be paying for pattern scanning first.
func (e *Engine) inspectRequest(w http.ResponseWriter, r *http.Request, rt *route, start time.Time) bool {
	if !rt.host.Guardian.Enabled() {
		return true
	}

	v := guardian.Inspect(&rt.host.Guardian, r)
	if !v.Matched {
		return true
	}

	blocked := rt.host.Guardian.Mode == domain.GuardianBlock
	level := slog.LevelWarn
	if !blocked {
		// In detect mode this is an observation an operator asked for, not
		// an incident, so it must not read like one in the log.
		level = slog.LevelInfo
	}
	e.logger.Log(r.Context(), level, "request matched an inspection rule",
		"host", rt.host.Name, "rule", string(v.Rule), "detail", v.Detail,
		"path", r.URL.Path, "client", e.clientIP(r), "blocked", blocked)

	if !blocked {
		return true
	}

	// The refusal says nothing about which rule fired. Telling a prober
	// exactly what tripped hands them the shape of the filter for free.
	e.finish(w, r, rt, start, http.StatusForbidden, "This request was refused.")
	return false
}
