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
	Timestamp      time.Time `json:"timestamp"`
	Requests       uint64    `json:"requests"`
	RequestsPerSec float64   `json:"requestsPerSec"`
	Status2xx      uint64    `json:"status2xx"`
	Status3xx      uint64    `json:"status3xx"`
	Status4xx      uint64    `json:"status4xx"`
	Status5xx      uint64    `json:"status5xx"`
	StatusError    uint64    `json:"statusError"`
	BytesIn        uint64    `json:"bytesIn"`
	BytesOut       uint64    `json:"bytesOut"`
	MeanLatencyMS  float64   `json:"meanLatencyMs"`
	MaxLatencyMS   uint64    `json:"maxLatencyMs"`
	ErrorRate      float64   `json:"errorRate"`
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
		Points:     toSeries(samples, resolution),
	})
}

// maxBuckets bounds one chart request. 5000 points is already far more than a
// screen can show.
const maxBuckets = 5000

func toSeries(samples []domain.Sample, resolution domain.Resolution) []seriesPoint {
	seconds := resolution.Duration().Seconds()
	points := make([]seriesPoint, 0, len(samples))

	for _, s := range samples {
		p := seriesPoint{
			Timestamp:      s.Timestamp,
			Requests:       s.Requests,
			RequestsPerSec: float64(s.Requests) / seconds,
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
