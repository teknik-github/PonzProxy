package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

type metricsRepo Store

// WriteSamples inserts one flush interval in a single transaction. A conflict
// on (host_id, ts) folds the counters together instead of failing, which makes
// the write idempotent under a retry after a partial commit.
func (r *metricsRepo) WriteSamples(ctx context.Context, samples []domain.Sample) error {
	if len(samples) == 0 {
		return nil
	}

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO metrics_samples (
				host_id, ts, requests, s2xx, s3xx, s4xx, s5xx, serr,
				bytes_in, bytes_out, lat_sum_ms, lat_max_ms
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (host_id, ts) DO UPDATE SET
				requests   = requests   + excluded.requests,
				s2xx       = s2xx       + excluded.s2xx,
				s3xx       = s3xx       + excluded.s3xx,
				s4xx       = s4xx       + excluded.s4xx,
				s5xx       = s5xx       + excluded.s5xx,
				serr       = serr       + excluded.serr,
				bytes_in   = bytes_in   + excluded.bytes_in,
				bytes_out  = bytes_out  + excluded.bytes_out,
				lat_sum_ms = lat_sum_ms + excluded.lat_sum_ms,
				lat_max_ms = MAX(lat_max_ms, excluded.lat_max_ms)`)
		if err != nil {
			return translateErr(err)
		}
		defer stmt.Close()

		for _, s := range samples {
			if _, err := stmt.ExecContext(ctx,
				s.HostID, s.Timestamp.Unix(), s.Requests,
				s.Status[domain.Status2xx], s.Status[domain.Status3xx],
				s.Status[domain.Status4xx], s.Status[domain.Status5xx],
				s.Status[domain.StatusError],
				s.BytesIn, s.BytesOut, s.LatencySumMS, s.LatencyMaxMS); err != nil {
				return translateErr(err)
			}
		}
		return nil
	})
}

// Query rolls raw samples up to the requested resolution in SQL. Bucketing by
// integer division on the unix timestamp keeps buckets aligned to absolute
// time, so a chart does not shift as the query window moves.
func (r *metricsRepo) Query(ctx context.Context, q domain.MetricsQuery) ([]domain.Sample, error) {
	if !q.Resolution.Valid() {
		return nil, fmt.Errorf("invalid resolution %q", string(q.Resolution))
	}
	if q.To.Before(q.From) {
		return nil, fmt.Errorf("query window ends before it starts")
	}

	bucket := int64(q.Resolution.Duration() / time.Second)
	args := []any{bucket, bucket, q.From.Unix(), q.To.Unix()}

	// host_id is grouped away when querying across all hosts, so the caller
	// gets one series either way.
	hostFilter := ""
	if q.HostID != 0 {
		hostFilter = " AND host_id = ?"
		args = append(args, q.HostID)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT (ts / ?) * ? AS bucket,
		       SUM(requests), SUM(s2xx), SUM(s3xx), SUM(s4xx), SUM(s5xx), SUM(serr),
		       SUM(bytes_in), SUM(bytes_out), SUM(lat_sum_ms), MAX(lat_max_ms)
		FROM metrics_samples
		WHERE ts >= ? AND ts <= ?`+hostFilter+`
		GROUP BY bucket
		ORDER BY bucket`, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	out := make([]domain.Sample, 0, 128)
	for rows.Next() {
		var (
			ts int64
			s  domain.Sample
		)
		if err := rows.Scan(&ts, &s.Requests,
			&s.Status[domain.Status2xx], &s.Status[domain.Status3xx],
			&s.Status[domain.Status4xx], &s.Status[domain.Status5xx],
			&s.Status[domain.StatusError],
			&s.BytesIn, &s.BytesOut, &s.LatencySumMS, &s.LatencyMaxMS); err != nil {
			return nil, translateErr(err)
		}
		s.HostID = q.HostID
		s.Timestamp = time.Unix(ts, 0).UTC()
		out = append(out, s)
	}
	return out, translateErr(rows.Err())
}

func (r *metricsRepo) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM metrics_samples WHERE ts < ?`, before.Unix())
	if err != nil {
		return 0, translateErr(err)
	}
	return res.RowsAffected()
}
