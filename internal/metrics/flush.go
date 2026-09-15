package metrics

import (
	"context"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// Run drives the collector's background duties: sampling traffic rates for the
// live console, folding counters into persisted samples, and pruning history
// past the retention window. It blocks until ctx is cancelled, then performs
// one final flush so the last interval before a shutdown is not lost.
func (c *Collector) Run(ctx context.Context, flushEvery, retention time.Duration) {
	flush := time.NewTicker(flushEvery)
	defer flush.Stop()

	// Pruning is cheap but not free, and retention is measured in days, so
	// once an hour is ample.
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()

	// The rate sampler runs here, and only here, so the live window is
	// consumed once per interval however many clients are watching.
	sample := time.NewTicker(rateInterval)
	defer sample.Stop()

	c.prune(ctx, retention)

	for {
		select {
		case <-ctx.Done():
			// ctx is already cancelled, so the final write needs its own
			// deadline or it would be refused before it starts.
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			c.flush(final)
			cancel()
			return
		case <-sample.C:
			c.sampleRates()
		case <-flush.C:
			c.flush(ctx)
		case <-prune.C:
			c.prune(ctx, retention)
		}
	}
}

// flush writes the traffic accumulated since the previous flush as one sample
// per active host.
func (c *Collector) flush(ctx context.Context) {
	now := time.Now().UTC().Truncate(time.Second)

	c.mu.Lock()
	samples := make([]domain.Sample, 0, len(c.hosts))
	for hostID, st := range c.hosts {
		current := st.counters.read()
		delta := current.sub(st.flushAt)
		st.flushAt = current

		// Hosts that saw no traffic contribute no row. Charts read the
		// gap as zero, which is both correct and far cheaper to store.
		if delta.requests == 0 {
			continue
		}
		samples = append(samples, domain.Sample{
			HostID:       hostID,
			Timestamp:    now,
			Requests:     delta.requests,
			Status:       [5]uint64(delta.status),
			BytesIn:      delta.bytesIn,
			BytesOut:     delta.bytesOut,
			LatencySumMS: delta.latSum,
			LatencyMaxMS: delta.latMax,
		})
	}
	c.mu.Unlock()

	if len(samples) == 0 {
		return
	}
	if err := c.repo.WriteSamples(ctx, samples); err != nil {
		// Losing a sample degrades a chart; it must never take the proxy
		// down, so this is logged and dropped.
		c.logger.Error("write metrics samples", "error", err, "samples", len(samples))
	}
}

func (c *Collector) prune(ctx context.Context, retention time.Duration) {
	if retention <= 0 {
		return
	}
	n, err := c.repo.Prune(ctx, time.Now().Add(-retention))
	if err != nil {
		c.logger.Error("prune metrics history", "error", err)
		return
	}
	if n > 0 {
		c.logger.Info("pruned metrics history", "rows", n, "retention", retention)
	}
}
