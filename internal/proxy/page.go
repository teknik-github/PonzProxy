package proxy

import (
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// writePage renders one of the proxy's own pages: maintenance, or an error
// when a backend cannot be reached.
//
// The markup is inlined rather than templated from a file, because these pages
// are served in exactly the circumstances where nothing else is working. A
// page that depends on reading a file, or on a stylesheet fetched from a
// backend that is by definition down, is a page that fails when it is needed.
//
// It is also why there is no operator-supplied HTML: the title and message are
// escaped and placed into a fixed layout. Letting an operator paste markup
// would let a stored mistake — or a stored script — be served from their own
// domain, with their own cookies in scope.
func writePage(w http.ResponseWriter, status int, title, message string, retryAfter int) int {
	body := renderPage(title, message, status)

	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A maintenance page cached by an intermediary outlives the maintenance
	// and takes the site down after it is over.
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.WriteHeader(status)
	n, _ := w.Write([]byte(body))
	return n
}

func renderPage(title, message string, status int) string {
	var b strings.Builder
	b.Grow(len(title) + len(message) + 1400)

	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<title>`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`</title><style>`)
	// A dark-mode-aware, system-font page with no external requests. The
	// visitor's browser already has everything it needs to render this.
	b.WriteString(`:root{color-scheme:light dark}` +
		`*{box-sizing:border-box}` +
		`body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;` +
		`padding:24px;background:#fafafa;color:#171717;` +
		`font:16px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}` +
		`main{max-width:32rem;text-align:center}` +
		`h1{margin:0 0 .75rem;font-size:1.5rem;font-weight:600;letter-spacing:-.01em}` +
		`p{margin:0;color:#525252}` +
		`.code{margin-top:2rem;font:12px ui-monospace,SFMono-Regular,Menlo,monospace;color:#a3a3a3}` +
		`@media(prefers-color-scheme:dark){body{background:#0a0a0a;color:#fafafa}` +
		`p{color:#a3a3a3}.code{color:#525252}}`)
	b.WriteString(`</style></head><body><main><h1>`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`</h1>`)
	if message != "" {
		b.WriteString(`<p>`)
		// Operators write more than one sentence and expect the break to
		// survive. Paragraphs are the only structure allowed through.
		b.WriteString(strings.ReplaceAll(html.EscapeString(message), "\n", "<br>"))
		b.WriteString(`</p>`)
	}
	b.WriteString(`<p class="code">`)
	b.WriteString(strconv.Itoa(status))
	b.WriteString(` &middot; ponzproxy</p></main></body></html>`)
	return b.String()
}

// serveMaintenance answers a host that is switched to maintenance. It reports
// whether the request was answered here, so the caller can stop.
//
// It runs before the traffic limits on purpose. A maintenance page is a few
// hundred bytes from memory and reaches no backend, so refusing it with 429
// would tell a visitor "too many requests" when the truth is "we are doing
// planned work" — worse information for the same amount of served traffic.
func (e *Engine) serveMaintenance(w http.ResponseWriter, r *http.Request, rt *route, start time.Time) bool {
	m := &rt.host.Maintenance
	if !m.Enabled {
		return false
	}
	// Whoever is performing the maintenance has to be able to check that it
	// worked, which means reaching the backend while the page is up.
	if m.Allows(e.clientAddr(r)) {
		return false
	}

	status := m.StatusCode
	if status == 0 {
		status = http.StatusServiceUnavailable
	}
	n := writePage(w, status, m.Title, m.Message, m.RetryAfterSeconds)

	// Recorded as served rather than failed: the proxy did exactly what it
	// was asked to. Counting planned maintenance as an error rate would set
	// off the alerting that the maintenance was scheduled to avoid.
	e.record(r, rt, start, status, int64(n), false)
	e.recordAccess(r, rt, nil, start, status, int64(n), nil)
	return true
}

// errorPage renders a host's own wording for an unreachable backend, or the
// built-in text when the host has not set any.
func errorPage(pages *domain.ErrorPages, status int, fallback string) (title, message string) {
	if pages != nil && pages.Enabled && pages.Title != "" {
		return pages.Title, pages.Message
	}
	return http.StatusText(status), fallback
}
