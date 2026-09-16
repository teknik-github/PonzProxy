package metrics

import (
	"runtime"
	"sort"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// rateInterval is how often traffic rates are recomputed. It sets the
// smoothing window for everything the console displays as "per second".
const rateInterval = time.Second

// sampleRates folds the counters into per-host and combined rates.
//
// It is the only place the live baseline advances, and Run is the only caller,
// so the window is consumed exactly once per interval no matter how many
// clients are watching. An earlier version computed rates inside Snapshot;
// with a dashboard polling every second and anything else reading the REST
// endpoint, each consumer stole the other's window and both saw nonsense.
func (c *Collector) sampleRates() {
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	var (
		combined      domain.TrafficSnapshot
		totalRequests uint64
		totalErrors   uint64
		latencySum    float64
	)

	for _, st := range c.hosts {
		current := st.counters.read()
		delta := current.sub(st.liveAt)
		elapsed := now.Sub(st.liveTime).Seconds()
		st.liveAt = current
		st.liveTime = now

		st.live = rates(delta, elapsed)
		st.live.ActiveConns = st.counters.active.Load()

		combined.RequestsPerSec += st.live.RequestsPerSec
		combined.BytesInPerSec += st.live.BytesInPerSec
		combined.BytesOutPerSec += st.live.BytesOutPerSec
		combined.ActiveConns += st.live.ActiveConns

		totalRequests += delta.requests
		totalErrors += delta.status[domain.Status5xx] + delta.status[domain.StatusError]
		latencySum += float64(delta.latSum)
	}

	if totalRequests > 0 {
		combined.MeanLatencyMS = latencySum / float64(totalRequests)
		combined.ErrorRate = float64(totalErrors) / float64(totalRequests)
	}
	c.totals = combined
}

// Snapshot renders the live state for the UI: the most recently sampled rates
// plus the current backend health from the proxy's pools.
//
// It is a pure read. Calling it twice in a row, or from several clients at
// once, returns the same rates rather than splitting one interval between
// them.
func (c *Collector) Snapshot() domain.Snapshot {
	pools := c.poolInfo()
	byID := make(map[int64]PoolInfo, len(pools))
	for _, p := range pools {
		byID[p.HostID] = p
	}

	c.mu.RLock()
	// Upstreams is a fresh slice per call, so filling in the rolling counts
	// here mutates nobody else's view. byID shares the same backing arrays,
	// so doing it once over pools covers both paths below.
	for _, p := range pools {
		c.applyShares(p.HostID, p.Upstreams)
	}
	hosts := make([]domain.HostSnapshot, 0, len(c.hosts))
	seen := make(map[int64]struct{}, len(c.hosts))

	for hostID, st := range c.hosts {
		seen[hostID] = struct{}{}
		info := byID[hostID]
		name := info.Name
		if name == "" {
			name = unmatchedName(hostID)
		}
		hosts = append(hosts, domain.HostSnapshot{
			HostID:    hostID,
			Name:      name,
			Traffic:   st.live,
			Upstreams: info.Upstreams,
		})
	}
	combined := c.totals
	c.mu.RUnlock()

	// A stable order keeps the UI from reordering rows on every tick, which
	// map iteration would otherwise do. The synthetic buckets sort last:
	// they are not hosts an operator configured, so they belong below the
	// ones that are.
	sort.Slice(hosts, func(i, j int) bool {
		a, b := hosts[i], hosts[j]
		if synthetic(a.HostID) != synthetic(b.HostID) {
			return !synthetic(a.HostID)
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.HostID < b.HostID
	})

	// Hosts with no traffic yet still belong on the dashboard, otherwise a
	// freshly configured host looks missing rather than idle.
	for _, p := range pools {
		if _, ok := seen[p.HostID]; ok {
			continue
		}
		hosts = append(hosts, domain.HostSnapshot{
			HostID: p.HostID, Name: p.Name, Upstreams: p.Upstreams,
		})
	}

	return domain.Snapshot{
		Timestamp:          time.Now().UTC(),
		Hosts:              hosts,
		Totals:             combined,
		System:             c.system(pools),
		ShareWindowSeconds: int(shareWindow / time.Second),
	}
}

func rates(d totals, elapsed float64) domain.TrafficSnapshot {
	var t domain.TrafficSnapshot
	if elapsed <= 0 {
		return t
	}
	t.RequestsPerSec = float64(d.requests) / elapsed
	t.BytesInPerSec = float64(d.bytesIn) / elapsed
	t.BytesOutPerSec = float64(d.bytesOut) / elapsed
	if d.requests > 0 {
		t.MeanLatencyMS = float64(d.latSum) / float64(d.requests)
		errs := d.status[domain.Status5xx] + d.status[domain.StatusError]
		t.ErrorRate = float64(errs) / float64(d.requests)
	}
	return t
}

func (c *Collector) system(pools []PoolInfo) domain.SystemSnapshot {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	s := domain.SystemSnapshot{
		UptimeSeconds: int64(time.Since(c.startedAt).Seconds()),
		Goroutines:    runtime.NumGoroutine(),
		HeapBytes:     mem.HeapAlloc,
		CPUPercent:    c.cpu.sample(),
	}
	for _, p := range pools {
		if p.Enabled {
			s.HostsEnabled++
		}
		s.UpstreamsUp += p.Up
		s.UpstreamsTotal += p.Total
	}
	return s
}

// synthetic reports whether a bucket stands for something other than a
// configured host.
func synthetic(hostID int64) bool { return hostID == UnmatchedHostID }

// unmatchedName labels the bucket that collects traffic for unknown domains.
func unmatchedName(hostID int64) string {
	if hostID == UnmatchedHostID {
		return "(unmatched)"
	}
	return "(deleted host)"
}
