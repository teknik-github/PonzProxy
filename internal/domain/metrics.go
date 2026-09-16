package domain

import "time"

// StatusClass buckets responses by their leading digit. Storing classes
// instead of individual codes keeps a month of per-host history small enough
// to query without an index scan.
type StatusClass int

const (
	Status2xx StatusClass = iota
	Status3xx
	Status4xx
	Status5xx
	// StatusError counts requests that never produced an upstream response
	// at all: connection refused, timeout, no healthy upstream.
	StatusError

	statusClassCount
)

// ClassifyStatus maps an HTTP status code to its bucket.
func ClassifyStatus(code int) StatusClass {
	switch {
	case code >= 200 && code < 300:
		return Status2xx
	case code >= 300 && code < 400:
		return Status3xx
	case code >= 400 && code < 500:
		return Status4xx
	case code >= 500:
		return Status5xx
	default:
		return StatusError
	}
}

// Sample is one rollup interval of traffic for a single host. Latency is kept
// as a sum plus a count so intervals can be re-aggregated into coarser
// buckets without losing the mean, alongside a max for tail visibility.
type Sample struct {
	HostID    int64     `json:"hostId"`
	Timestamp time.Time `json:"timestamp"`

	Requests uint64 `json:"requests"`
	// Status counts are indexed by StatusClass.
	Status [statusClassCount]uint64 `json:"status"`

	BytesIn  uint64 `json:"bytesIn"`
	BytesOut uint64 `json:"bytesOut"`

	LatencySumMS uint64 `json:"latencySumMs"`
	LatencyMaxMS uint64 `json:"latencyMaxMs"`
}

// MeanLatencyMS is the average request duration over the interval.
func (s Sample) MeanLatencyMS() float64 {
	if s.Requests == 0 {
		return 0
	}
	return float64(s.LatencySumMS) / float64(s.Requests)
}

// Snapshot is the live state pushed to the UI over WebSocket. Unlike Sample it
// is never persisted — it describes this instant, not an interval.
type Snapshot struct {
	Timestamp time.Time       `json:"timestamp"`
	Hosts     []HostSnapshot  `json:"hosts"`
	Totals    TrafficSnapshot `json:"totals"`
	System    SystemSnapshot  `json:"system"`
	// ShareWindowSeconds is the span UpstreamSnapshot.WindowRequests covers.
	// The UI reports it rather than assuming a number, so the label and the
	// measurement cannot drift apart.
	ShareWindowSeconds int `json:"shareWindowSeconds"`
}

type HostSnapshot struct {
	HostID    int64              `json:"hostId"`
	Name      string             `json:"name"`
	Traffic   TrafficSnapshot    `json:"traffic"`
	Upstreams []UpstreamSnapshot `json:"upstreams"`
}

// UpstreamSnapshot exposes the per-backend runtime state the balancer keeps,
// which is what makes a load balancer UI worth watching.
type UpstreamSnapshot struct {
	UpstreamID int64  `json:"upstreamId"`
	Address    string `json:"address"`
	Healthy    bool   `json:"healthy"`
	Enabled    bool   `json:"enabled"`
	Weight     int    `json:"weight"`
	// Ejected reports that passive health took this backend out of rotation
	// after repeated connection failures on real traffic. It is distinct
	// from Healthy, which reflects the active probe, because an operator
	// needs to know which of the two removed the backend.
	Ejected bool `json:"ejected"`
	// EjectedForSeconds is how much longer the ejection lasts.
	EjectedForSeconds int64 `json:"ejectedForSeconds,omitempty"`
	// ActiveConns is the number of requests in flight right now — the value
	// LeastConnections selects on.
	ActiveConns int64 `json:"activeConns"`
	// TotalRequests is cumulative since process start.
	TotalRequests uint64 `json:"totalRequests"`
	// WindowRequests is how many of those arrived in the last
	// Snapshot.ShareWindowSeconds. Cumulative totals answer "how has this
	// pool behaved", which is not the same question as "where is traffic
	// going now" — and after an algorithm change the two disagree for as
	// long as the history is heavier than the present.
	WindowRequests uint64  `json:"windowRequests"`
	MeanLatencyMS  float64 `json:"meanLatencyMs"`
	LastError      string  `json:"lastError,omitempty"`
}

type TrafficSnapshot struct {
	RequestsPerSec float64 `json:"requestsPerSec"`
	BytesInPerSec  float64 `json:"bytesInPerSec"`
	BytesOutPerSec float64 `json:"bytesOutPerSec"`
	MeanLatencyMS  float64 `json:"meanLatencyMs"`
	ErrorRate      float64 `json:"errorRate"`
	ActiveConns    int64   `json:"activeConns"`
}

type SystemSnapshot struct {
	UptimeSeconds  int64   `json:"uptimeSeconds"`
	Goroutines     int     `json:"goroutines"`
	HeapBytes      uint64  `json:"heapBytes"`
	CPUPercent     float64 `json:"cpuPercent"`
	HostsEnabled   int     `json:"hostsEnabled"`
	UpstreamsUp    int     `json:"upstreamsUp"`
	UpstreamsTotal int     `json:"upstreamsTotal"`
}
