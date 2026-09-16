package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// seriesPoint is one bucket of the historical chart. Rates are precomputed
// here because only the server knows how wide each bucket is.
type seriesPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Requests  uint64    `json:"requests"`
	// RequestsPerSec is Requests over the part of the bucket that has
	// actually elapsed, which for every bucket but the last is its whole
	// width. See Partial.
	RequestsPerSec float64 `json:"requestsPerSec"`
	// Partial marks a bucket that is still filling. Its Requests count is
	// a fraction of what the bucket will hold, so plotting the count makes
	// live traffic look like it fell off a cliff at the right-hand edge of
	// every chart. The rate does not have that problem, which is why the
	// chart draws the rate and the tooltip shows both.
	Partial       bool    `json:"partial"`
	Status2xx     uint64  `json:"status2xx"`
	Status3xx     uint64  `json:"status3xx"`
	Status4xx     uint64  `json:"status4xx"`
	Status5xx     uint64  `json:"status5xx"`
	StatusError   uint64  `json:"statusError"`
	BytesIn       uint64  `json:"bytesIn"`
	BytesOut      uint64  `json:"bytesOut"`
	MeanLatencyMS float64 `json:"meanLatencyMs"`
	MaxLatencyMS  uint64  `json:"maxLatencyMs"`
	ErrorRate     float64 `json:"errorRate"`
}

type seriesResponse struct {
	HostID     int64             `json:"hostId"`
	From       time.Time         `json:"from"`
	To         time.Time         `json:"to"`
	Resolution domain.Resolution `json:"resolution"`
	Points     []seriesPoint     `json:"points"`
}

// handleMetricsHistory serves the historical charts.
//
// Query parameters: hostId (0 or absent means every host combined), from and
// to as RFC3339, and resolution. Defaults cover the last hour at minute
// resolution, which is what the dashboard opens with.
func (s *Server) handleMetricsHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	to := time.Now().UTC()
	from := to.Add(-time.Hour)
	resolution := domain.ResolutionMinute

	if v := q.Get("from"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, s.logger, errors.Join(errBadRequest,
				errors.New("from must be an RFC3339 timestamp")))
			return
		}
		from = parsed.UTC()
	}
	if v := q.Get("to"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, s.logger, errors.Join(errBadRequest,
				errors.New("to must be an RFC3339 timestamp")))
			return
		}
		to = parsed.UTC()
	}
	if v := q.Get("resolution"); v != "" {
		resolution = domain.Resolution(v)
		if !resolution.Valid() {
			writeError(w, s.logger, errors.Join(errBadRequest,
				errors.New("resolution must be minute, hour or day")))
			return
		}
	}

	var hostID int64
	if v := q.Get("hostId"); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, s.logger, errors.Join(errBadRequest,
				errors.New("hostId must be a number")))
			return
		}
		hostID = parsed
	}

	if !to.After(from) {
		writeError(w, s.logger, errors.Join(errBadRequest,
			errors.New("to must be later than from")))
		return
	}
	// An unbounded window at minute resolution could ask SQLite for
	// hundreds of thousands of buckets and hand the browser a chart it
	// cannot draw. The cap is per resolution rather than absolute.
	if buckets := to.Sub(from) / resolution.Duration(); buckets > maxBuckets {
		writeError(w, s.logger, errors.Join(errBadRequest,
			errors.New("the requested window is too long for this resolution; "+
				"use a coarser resolution or a shorter window")))
		return
	}

	samples, err := s.opts.Metrics.Query(r.Context(), domain.MetricsQuery{
		HostID: hostID, From: from, To: to, Resolution: resolution,
	})
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	writeJSON(w, s.logger, http.StatusOK, seriesResponse{
		HostID:     hostID,
		From:       from,
		To:         to,
		Resolution: resolution,
		Points:     toSeries(samples, resolution, s.historyNow(), s.flushInterval()),
	})
}

// historyNow is the instant history actually reaches.
//
// Not time.Now(): counters are folded into samples on an interval, so the
// newest few seconds of traffic have not been written yet. Dividing the
// still-filling bucket by the time since it started would therefore report a
// rate low by however long ago the last flush was — a small, permanent dip on
// the right-hand edge of every chart.
func (s *Server) historyNow() time.Time {
	if s.opts.Collector != nil {
		if at := s.opts.Collector.LastFlush(); !at.IsZero() {
			return at
		}
	}
	return time.Now().UTC()
}

// partialSpan is roughly how much traffic time the newest bucket represents.
//
// Only roughly, and that is the point. Counters are folded into a sample on a
// fixed interval and stamped with the moment of the flush, so how much traffic
// a half-open bucket holds depends on where the flush ticker's phase happens
// to sit relative to the bucket boundary — anywhere across a whole interval.
// An earlier version of this tried to model that and produced a newest point
// that read 61/s among neighbours at 47/s, then one that climbed through the
// minute to meet them. Neither was a number worth plotting.
//
// So the estimate stays deliberately simple, the bucket is marked Partial, and
// the chart leaves it out. The figure is still worth returning: a caller that
// wants "how is the current minute going" gets something far closer than the
// raw count divided by the nominal width.
func partialSpan(bucketStart, through time.Time, width, flush time.Duration) time.Duration {
	span := through.Sub(bucketStart)
	if span > width {
		return width
	}
	// A bucket that exists at all has had at least one flush land in it.
	if span < flush {
		return flush
	}
	return span
}

// flushInterval is how often counters become a sample.
func (s *Server) flushInterval() time.Duration {
	if s.opts.MetricsFlush > 0 {
		return s.opts.MetricsFlush
	}
	return 10 * time.Second
}

// maxBuckets bounds one chart request. 5000 points is already far more than a
// screen can show.
const maxBuckets = 5000

// toSeries turns stored samples into chart points.
//
// through is the instant the stored data actually reaches — the last flush,
// not the wall clock — and flush is how often counters are folded into a
// sample. Both are needed to say how much time the newest bucket represents;
// see partialSpan.
func toSeries(samples []domain.Sample, resolution domain.Resolution,
	through time.Time, flush time.Duration) []seriesPoint {

	width := resolution.Duration()
	points := make([]seriesPoint, 0, len(samples))

	for _, s := range samples {
		// A bucket whose end is still in the future holds only the part of
		// it that has happened. Dividing by the full width would report a
		// rate that falls steadily towards the start of every minute and
		// recovers by the end of it — an artefact, not traffic.
		elapsed := width.Seconds()
		partial := s.Timestamp.Add(width).After(through)
		if partial {
			elapsed = partialSpan(s.Timestamp, through, width, flush).Seconds()
		}
		if elapsed <= 0 {
			elapsed = width.Seconds()
		}

		p := seriesPoint{
			Timestamp:      s.Timestamp,
			Requests:       s.Requests,
			Partial:        partial,
			RequestsPerSec: float64(s.Requests) / elapsed,
			Status2xx:      s.Status[domain.Status2xx],
			Status3xx:      s.Status[domain.Status3xx],
			Status4xx:      s.Status[domain.Status4xx],
			Status5xx:      s.Status[domain.Status5xx],
			StatusError:    s.Status[domain.StatusError],
			BytesIn:        s.BytesIn,
			BytesOut:       s.BytesOut,
			MeanLatencyMS:  s.MeanLatencyMS(),
			MaxLatencyMS:   s.LatencyMaxMS,
		}
		if s.Requests > 0 {
			errs := s.Status[domain.Status5xx] + s.Status[domain.StatusError]
			p.ErrorRate = float64(errs) / float64(s.Requests)
		}
		points = append(points, p)
	}
	return points
}

// handleLiveSnapshot returns the same payload the WebSocket pushes, for
// clients that would rather poll.
func (s *Server) handleLiveSnapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.logger, http.StatusOK, s.opts.Collector.Snapshot())
}

// usageRow is one host's traffic over the reported window.
type usageRow struct {
	HostID   int64  `json:"hostId"`
	Name     string `json:"name"`
	Requests uint64 `json:"requests"`
	BytesIn  uint64 `json:"bytesIn"`
	BytesOut uint64 `json:"bytesOut"`
	Total    uint64 `json:"totalBytes"`
}

type usageResponse struct {
	From time.Time  `json:"from"`
	To   time.Time  `json:"to"`
	Rows []usageRow `json:"rows"`
	// Truncated says the window reaches back further than the samples do,
	// so the figures cover less time than was asked for. An operator
	// comparing a month against their hosting bill needs to be told that
	// rather than left to wonder why the number is low.
	Truncated bool `json:"truncated"`
	// RetentionDays is how far back samples are kept at all.
	RetentionDays int `json:"retentionDays"`
}

// handleMetricsUsage reports how much traffic each host carried over a window.
//
// Query parameters: from and to as RFC3339, defaulting to the last 30 days.
// The figures come from the same samples as the historical charts, so they
// stop where retention does.
func (s *Server) handleMetricsUsage(w http.ResponseWriter, r *http.Request) {
	from, to, err := usageWindow(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	report, err := s.usageReport(r, from, to)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, report)
}

// usageReport gathers the figures the JSON, spreadsheet and PDF views all
// render. Keeping it in one place is what stops a download from disagreeing
// with the screen it was started from.
func (s *Server) usageReport(r *http.Request, from, to time.Time) (usageResponse, error) {
	rows, err := s.opts.Metrics.Usage(r.Context(), from, to)
	if err != nil {
		return usageResponse{}, err
	}

	// Names come from configuration rather than from the samples, which
	// only carry ids. A host deleted since the traffic happened still has
	// usage worth reporting, so it is labelled rather than dropped.
	names := map[int64]string{}
	if hosts, err := s.opts.Hosts.List(r.Context()); err == nil {
		for _, h := range hosts {
			names[h.ID] = h.Name
		}
	}

	out := make([]usageRow, 0, len(rows))
	for _, u := range rows {
		name := names[u.HostID]
		if name == "" {
			name = usageLabel(u.HostID)
		}
		out = append(out, usageRow{
			HostID:   u.HostID,
			Name:     name,
			Requests: u.Requests,
			BytesIn:  u.BytesIn,
			BytesOut: u.BytesOut,
			Total:    u.BytesIn + u.BytesOut,
		})
	}

	retention := s.opts.MetricsRetention
	return usageResponse{
		From:          from,
		To:            to,
		Rows:          out,
		Truncated:     retention > 0 && to.Sub(from) > retention,
		RetentionDays: int(retention.Hours() / 24),
	}, nil
}

// usageLabel names a bucket that no longer has a host behind it.
func usageLabel(hostID int64) string {
	if hostID == 0 {
		return "(unmatched)"
	}
	return "(deleted host " + strconv.FormatInt(hostID, 10) + ")"
}
