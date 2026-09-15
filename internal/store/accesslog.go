package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

type accessLogRepo Store

// AccessLog returns the access log repository.
func (s *Store) AccessLog() domain.AccessLogRepository { return (*accessLogRepo)(s) }

// Write appends a batch in one transaction. Batching is what makes access
// logging affordable: one commit per flush rather than one per request.
func (r *accessLogRepo) Write(ctx context.Context, entries []domain.AccessLogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO access_log (
				ts, host_id, method, path, status, duration_ms, bytes_out,
				client_ip, upstream, user_agent, error
			) VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return translateErr(err)
		}
		defer stmt.Close()

		for _, e := range entries {
			if _, err := stmt.ExecContext(ctx,
				e.Timestamp.UnixMilli(), e.HostID, e.Method, e.Path, e.Status,
				e.DurationMS, e.BytesOut, e.ClientIP, e.Upstream,
				e.UserAgent, e.Error); err != nil {
				return translateErr(err)
			}
		}
		return nil
	})
}

// Query returns one page newest first, plus the total number matching.
//
// The total is a second query rather than a window function: SQLite has to
// scan for it either way, and keeping them apart means the page query can stop
// at LIMIT rows.
func (r *accessLogRepo) Query(ctx context.Context, q domain.AccessLogQuery) ([]domain.AccessLogEntry, int, error) {
	q.Normalize()

	where := []string{"ts >= ?", "ts <= ?"}
	args := []any{q.From.UnixMilli(), q.To.UnixMilli()}

	if q.HostID != 0 {
		where = append(where, "host_id = ?")
		args = append(args, q.HostID)
	}
	if q.StatusClass != 0 {
		// A band rather than an exact code: an operator looks for "the 5xx",
		// not for 503 specifically.
		low := q.StatusClass * 100
		where = append(where, "status >= ? AND status < ?")
		args = append(args, low, low+100)
	}
	if q.FailedOnly {
		where = append(where, "error != ''")
	}
	if q.Search != "" {
		// LIKE with a leading wildcard cannot use an index, which is why
		// the time range is always bounded first.
		// ESCAPE is required: without it SQLite treats the backslashes
		// escapeLike inserts as literals, and a search for "100%" would
		// match every row.
		where = append(where, `(path LIKE ? ESCAPE '\' OR client_ip LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLike(q.Search) + "%"
		args = append(args, pattern, pattern)
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	var total int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM access_log`+clause, args...).Scan(&total); err != nil {
		return nil, 0, translateErr(err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, ts, host_id, method, path, status, duration_ms, bytes_out,
		       client_ip, upstream, user_agent, error
		FROM access_log`+clause+`
		ORDER BY ts DESC, id DESC
		LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, translateErr(err)
	}
	defer rows.Close()

	out := make([]domain.AccessLogEntry, 0, q.Limit)
	for rows.Next() {
		var (
			e  domain.AccessLogEntry
			ms int64
		)
		if err := rows.Scan(&e.ID, &ms, &e.HostID, &e.Method, &e.Path, &e.Status,
			&e.DurationMS, &e.BytesOut, &e.ClientIP, &e.Upstream,
			&e.UserAgent, &e.Error); err != nil {
			return nil, 0, translateErr(err)
		}
		e.Timestamp = time.UnixMilli(ms).UTC()
		out = append(out, e)
	}
	return out, total, translateErr(rows.Err())
}

// Prune enforces retention twice over: by age, and by a hard row cap.
//
// The cap is the one that matters. Age alone cannot bound the table — a burst
// of traffic inside the retention window can still fill a disk — so the row
// limit is what makes the worst case predictable.
func (r *accessLogRepo) Prune(ctx context.Context, before time.Time, maxRows int) (int64, error) {
	var removed int64

	res, err := r.db.ExecContext(ctx,
		`DELETE FROM access_log WHERE ts < ?`, before.UnixMilli())
	if err != nil {
		return 0, translateErr(err)
	}
	if n, err := res.RowsAffected(); err == nil {
		removed += n
	}

	if maxRows > 0 {
		// Keep the newest maxRows. The subquery is bounded by LIMIT, so it
		// does not materialise the whole table.
		res, err := r.db.ExecContext(ctx, `
			DELETE FROM access_log
			WHERE id NOT IN (
				SELECT id FROM access_log ORDER BY ts DESC, id DESC LIMIT ?
			)`, maxRows)
		if err != nil {
			return removed, translateErr(err)
		}
		if n, err := res.RowsAffected(); err == nil {
			removed += n
		}
	}
	return removed, nil
}

// escapeLike neutralises the wildcards a caller's search text may contain, so
// a query for "100%" does not match everything.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
