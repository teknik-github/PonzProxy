package proxy

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// enforceAccess applies the host's access list before any upstream is chosen.
// It reports whether the request may continue.
func (e *Engine) enforceAccess(w http.ResponseWriter, r *http.Request, rt *route, start time.Time) bool {
	if rt.access == nil {
		return true
	}

	client := e.clientIP(r)
	user, password, present := r.BasicAuth()

	// A credential verified moments ago skips bcrypt entirely. The address
	// rules still run: they are cheap, and they must not be cacheable per
	// credential — the same password from a newly denied address has to be
	// refused.
	var key authKey
	cached := false
	if present && len(rt.access.BasicAuth) > 0 {
		key = makeAuthKey(rt.access.ID, rt.access.UpdatedAt, user, password)
		cached = e.authCache.granted(key, time.Now())
	}

	creds := domain.BasicCredentials{Username: user, Password: password, Present: present}
	if cached {
		// Stand in for the verified password so the address half, and the
		// satisfy-any combination, are still evaluated normally.
		creds.Verified = true
	}

	decision := domain.EvaluateAccess(rt.access, client, creds)
	if decision == domain.AccessGranted {
		if present && !cached && len(rt.access.BasicAuth) > 0 {
			e.authCache.remember(key, time.Now())
		}
		return true
	}

	status, msg := http.StatusForbidden, "Access to this host is not allowed from your address."
	level := slog.LevelInfo
	if decision == domain.AccessNeedsCredentials {
		// The realm is a constant: interpolating the list's name would let
		// an operator's typing choose the bytes of a response header.
		w.Header().Set("WWW-Authenticate", `Basic realm="Restricted", charset="UTF-8"`)
		status, msg = http.StatusUnauthorized, "Authentication is required for this host."
		// Every first request to a protected host lands here, so a refusal
		// is news and a challenge is not.
		level = slog.LevelDebug
	}

	e.logger.Log(r.Context(), level, "request refused by access list",
		"host", rt.host.Name, "list", rt.access.Name,
		"client", client, "decision", decision.String(), "status", status)
	e.finish(w, r, rt, start, status, msg)
	return false
}
