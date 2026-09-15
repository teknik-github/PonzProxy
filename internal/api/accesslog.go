package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/accesslog"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// accessLogResponse is one page of recorded requests.
//
// Total travels with the page so the console can say how much it is not
// showing, and Stats travels with it so an operator can tell a quiet host from
// a writer that is dropping entries — the two look identical otherwise.
type accessLogResponse struct {
	Entries []domain.AccessLogEntry `json:"entries"`
	Total   int                     `json:"total"`
	Limit   int                     `json:"limit"`
	Offset  int                     `json:"offset"`
	Stats   accesslog.Stats         `json:"stats"`
}

func (s *Server) handleAccessLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	query := domain.AccessLogQuery{
		Search:     q.Get("search"),
		FailedOnly: q.Get("failedOnly") == "true",
	}

	for _, p := range []struct {
		name string
		dest *int64
	}{{"hostId", &query.HostID}} {
		if v := q.Get(p.name); v != "" {
			parsed, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				writeError(w, s.logger, errors.Join(errBadRequest,
					errors.New(p.name+" must be a number")))
				return
			}
			*p.dest = parsed
		}
	}

	if v := q.Get("statusClass"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 2 || parsed > 5 {
			writeError(w, s.logger, errors.Join(errBadRequest,
				errors.New("statusClass must be 2, 3, 4 or 5")))
			return
		}
		query.StatusClass = parsed
	}

	for _, p := range []struct {
		name string
		dest *time.Time
	}{{"from", &query.From}, {"to", &query.To}} {
		if v := q.Get(p.name); v != "" {
			parsed, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, s.logger, errors.Join(errBadRequest,
					errors.New(p.name+" must be an RFC3339 timestamp")))
				return
			}
			*p.dest = parsed
		}
	}

	for _, p := range []struct {
		name string
		dest *int
	}{{"limit", &query.Limit}, {"offset", &query.Offset}} {
		if v := q.Get(p.name); v != "" {
			parsed, err := strconv.Atoi(v)
			if err != nil || parsed < 0 {
				writeError(w, s.logger, errors.Join(errBadRequest,
					errors.New(p.name+" must be a non-negative number")))
				return
			}
			*p.dest = parsed
		}
	}

	// Normalize bounds the page size, so no caller can ask for a scan of the
	// whole table.
	query.Normalize()

	entries, total, err := s.opts.AccessLogs.Query(r.Context(), query)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	var stats accesslog.Stats
	if s.opts.AccessLogStats != nil {
		stats = s.opts.AccessLogStats()
	}

	writeJSON(w, s.logger, http.StatusOK, accessLogResponse{
		Entries: entries,
		Total:   total,
		Limit:   query.Limit,
		Offset:  query.Offset,
		Stats:   stats,
	})
}
