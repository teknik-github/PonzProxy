package domain

import (
	"context"
	"strings"
	"time"
)

// AccessLogEntry is one proxied request as recorded for later search.
//
// It deliberately carries less than the process log: no request headers, no
// bodies, and — unless the host opts in — no query string. A searchable store
// behind a web console is a different exposure from a root-only file, and
// query strings routinely carry session tokens, API keys and password-reset
// links.
type AccessLogEntry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	HostID    int64     `json:"hostId"`

	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`

	DurationMS int64 `json:"durationMs"`
	BytesOut   int64 `json:"bytesOut"`

	ClientIP string `json:"clientIp"`
	// Upstream is the backend that served it, empty when none was reached.
	Upstream  string `json:"upstream"`
	UserAgent string `json:"userAgent"`
	// Error explains a request that never got an upstream response.
	Error string `json:"error,omitempty"`
}

// maxLoggedField bounds the text columns. A client controls the path and the
// user agent, and without a cap either could be megabytes per request.
const maxLoggedField = 512

// Truncate clamps the client-controlled fields to a sane length. The writer
// calls it so no oversized row ever reaches the database.
func (e *AccessLogEntry) Truncate() {
	e.Path = clamp(e.Path, maxLoggedField)
	e.UserAgent = clamp(e.UserAgent, maxLoggedField)
	e.Error = clamp(e.Error, maxLoggedField)
	e.ClientIP = clamp(e.ClientIP, 64)
	e.Method = clamp(e.Method, 16)
	e.Upstream = clamp(e.Upstream, 128)
}

func clamp(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Cut on a rune boundary so the stored value stays valid UTF-8.
	cut := max
	for cut > 0 && !isUTF8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// AccessLogSettings is the per-host switch. Logging is off by default: a busy
// proxy would otherwise fill a disk with rows nobody asked for.
type AccessLogSettings struct {
	Enabled bool `json:"enabled"`
	// IncludeQuery adds the query string to the recorded path. Off by
	// default — see AccessLogEntry for why.
	IncludeQuery bool `json:"includeQuery"`
}

// StatusClass filters a query to one band of status codes.
type AccessLogQuery struct {
	// HostID of 0 means every host.
	HostID   int64
	From, To time.Time
	// StatusClass is 2, 3, 4 or 5 to select a band; 0 means any.
	StatusClass int
	// Search matches a substring of the path or the client address.
	Search string
	// FailedOnly narrows to requests that never reached an upstream.
	FailedOnly bool

	Limit, Offset int
}

// Normalize applies the defaults and bounds a query, so no caller can ask for
// an unbounded scan.
func (q *AccessLogQuery) Normalize() {
	q.Search = strings.TrimSpace(q.Search)
	if q.Limit <= 0 || q.Limit > MaxAccessLogPage {
		q.Limit = DefaultAccessLogPage
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.StatusClass != 0 && (q.StatusClass < 2 || q.StatusClass > 5) {
		q.StatusClass = 0
	}
	if q.To.IsZero() {
		q.To = time.Now()
	}
	if q.From.IsZero() {
		q.From = q.To.Add(-24 * time.Hour)
	}
}

const (
	DefaultAccessLogPage = 100
	MaxAccessLogPage     = 500
)

// AccessLogRepository stores and searches proxied requests.
type AccessLogRepository interface {
	// Write appends a batch. It is called from the log writer's own
	// goroutine, never from a request.
	Write(ctx context.Context, entries []AccessLogEntry) error
	// Query returns a page of entries newest first, plus the total number
	// matching, so the UI can show how much it is not displaying.
	Query(ctx context.Context, q AccessLogQuery) (entries []AccessLogEntry, total int, err error)
	// Prune enforces retention by age and by row count, and reports how
	// many rows it removed.
	Prune(ctx context.Context, before time.Time, maxRows int) (int64, error)
}
