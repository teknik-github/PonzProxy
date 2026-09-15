package metrics

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// fakeRepo captures what the flush loop writes.
type fakeRepo struct {
	mu      sync.Mutex
	samples []domain.Sample
	pruned  []time.Time
}

func (f *fakeRepo) WriteSamples(_ context.Context, s []domain.Sample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.samples = append(f.samples, s...)
	return nil
}

func (f *fakeRepo) Query(context.Context, domain.MetricsQuery) ([]domain.Sample, error) {
	return nil, nil
}

func (f *fakeRepo) Prune(_ context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned = append(f.pruned, before)
	return 0, nil
}

func (f *fakeRepo) written() []domain.Sample {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Sample(nil), f.samples...)
}

func newTestCollector(repo domain.MetricsRepository) *Collector {
	return New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestFlushWritesDeltasNotTotals(t *testing.T) {
	repo := &fakeRepo{}
	c := newTestCollector(repo)
	ctx := context.Background()

	for range 3 {
		c.Record(Result{HostID: 7, Status: 200, BytesIn: 10, BytesOut: 100, Duration: 20 * time.Millisecond})
	}
	c.flush(ctx)

	got := repo.written()
	if len(got) != 1 {
		t.Fatalf("got %d samples, want 1", len(got))
	}
	if got[0].Requests != 3 || got[0].Status[domain.Status2xx] != 3 {
		t.Errorf("first flush = %+v, want 3 requests all 2xx", got[0])
	}
	if got[0].BytesOut != 300 || got[0].LatencySumMS != 60 {
		t.Errorf("bytes/latency = %d/%d, want 300/60", got[0].BytesOut, got[0].LatencySumMS)
	}

	// A second flush with one more request must report only that request.
	c.Record(Result{HostID: 7, Status: 500, Duration: time.Millisecond})
	c.flush(ctx)

	got = repo.written()
	if len(got) != 2 {
		t.Fatalf("got %d samples, want 2", len(got))
	}
	if got[1].Requests != 1 || got[1].Status[domain.Status5xx] != 1 {
		t.Errorf("second flush = %+v, want exactly the one new 5xx", got[1])
	}

	// An idle interval writes nothing at all.
	c.flush(ctx)
	if n := len(repo.written()); n != 2 {
		t.Errorf("idle flush wrote a row: %d samples", n)
	}
}

func TestFailedRequestsCountAsErrors(t *testing.T) {
	repo := &fakeRepo{}
	c := newTestCollector(repo)

	// A failed request never reached an upstream, so the 502 the client saw
	// must not be filed as an ordinary 5xx from a backend.
	c.Record(Result{HostID: 1, Status: 502, Failed: true, Duration: time.Millisecond})
	c.flush(context.Background())

	got := repo.written()[0]
	if got.Status[domain.StatusError] != 1 {
		t.Errorf("error bucket = %d, want 1", got.Status[domain.StatusError])
	}
	if got.Status[domain.Status5xx] != 0 {
		t.Errorf("5xx bucket = %d, want 0", got.Status[domain.Status5xx])
	}
}

func TestSnapshotComputesRatesIndependentlyOfFlush(t *testing.T) {
	repo := &fakeRepo{}
	c := newTestCollector(repo)

	c.sampleRates() // establish the live baseline
	for range 10 {
		c.Record(Result{HostID: 1, Status: 200, Duration: 10 * time.Millisecond})
	}
	time.Sleep(60 * time.Millisecond)

	c.sampleRates()
	snap := c.Snapshot()
	if len(snap.Hosts) != 1 {
		t.Fatalf("got %d hosts, want 1", len(snap.Hosts))
	}
	if rps := snap.Hosts[0].Traffic.RequestsPerSec; rps <= 0 {
		t.Errorf("requests/sec = %v, want a positive rate", rps)
	}
	if mean := snap.Hosts[0].Traffic.MeanLatencyMS; mean != 10 {
		t.Errorf("mean latency = %v, want 10", mean)
	}

	// Flushing must not disturb the live baseline, and vice versa.
	c.flush(context.Background())
	if got := repo.written(); len(got) != 1 || got[0].Requests != 10 {
		t.Errorf("flush after snapshot = %+v, want all 10 requests", got)
	}

	// A fresh sample with no traffic reports a zero rate, not a repeat.
	time.Sleep(20 * time.Millisecond)
	c.sampleRates()
	if rps := c.Snapshot().Hosts[0].Traffic.RequestsPerSec; rps != 0 {
		t.Errorf("idle requests/sec = %v, want 0", rps)
	}
}

// TestSnapshotDoesNotConsumeTheRateWindow pins down the bug that made the REST
// endpoint unusable whenever a dashboard was open: both called Snapshot, each
// advanced the live baseline, and whichever ran second divided a near-empty
// delta by a near-zero interval — reporting either nothing or a rate hundreds
// of times too high.
func TestSnapshotDoesNotConsumeTheRateWindow(t *testing.T) {
	c := newTestCollector(&fakeRepo{})

	c.sampleRates()
	for range 10 {
		c.Record(Result{HostID: 1, Status: 200, Duration: 10 * time.Millisecond})
	}
	time.Sleep(60 * time.Millisecond)
	c.sampleRates()

	first := c.Snapshot()
	second := c.Snapshot() // immediately after, as a second client would
	third := c.Snapshot()

	if first.Totals.RequestsPerSec <= 0 {
		t.Fatalf("requests/sec = %v, want a positive rate", first.Totals.RequestsPerSec)
	}
	for i, got := range []domain.Snapshot{second, third} {
		if got.Totals.RequestsPerSec != first.Totals.RequestsPerSec {
			t.Errorf("reader %d saw %v req/s, want the same %v as the first — "+
				"reading a snapshot must not consume the window",
				i+2, got.Totals.RequestsPerSec, first.Totals.RequestsPerSec)
		}
		if len(got.Hosts) == 0 || got.Hosts[0].Traffic.RequestsPerSec != first.Hosts[0].Traffic.RequestsPerSec {
			t.Errorf("reader %d saw a different per-host rate", i+2)
		}
	}
}

// TestRunSamplesRatesOnItsOwn checks the wiring: rates must appear without
// anyone calling sampleRates by hand.
func TestRunSamplesRatesOnItsOwn(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx, time.Hour, 24*time.Hour) }()

	for range 20 {
		c.Record(Result{HostID: 1, Status: 200, Duration: 5 * time.Millisecond})
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Snapshot().Totals.RequestsPerSec > 0 {
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("Run never sampled a traffic rate")
}

func TestSnapshotReportsErrorRateAndActiveConns(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	c.sampleRates()

	for range 3 {
		c.Record(Result{HostID: 1, Status: 200, Duration: time.Millisecond})
	}
	c.Record(Result{HostID: 1, Status: 503, Duration: time.Millisecond})
	c.RequestStarted(1)
	c.RequestStarted(1)
	c.RequestFinished(1)

	time.Sleep(20 * time.Millisecond)
	c.sampleRates()
	snap := c.Snapshot()

	if got := snap.Totals.ErrorRate; got != 0.25 {
		t.Errorf("error rate = %v, want 0.25", got)
	}
	if got := snap.Hosts[0].Traffic.ActiveConns; got != 1 {
		t.Errorf("active conns = %d, want 1", got)
	}
}

func TestForgetKeepsUnmatchedBucket(t *testing.T) {
	c := newTestCollector(&fakeRepo{})
	c.Record(Result{HostID: UnmatchedHostID, Status: 404, Duration: time.Millisecond})
	c.Record(Result{HostID: 1, Status: 200, Duration: time.Millisecond})
	c.Record(Result{HostID: 2, Status: 200, Duration: time.Millisecond})

	c.Forget(map[int64]struct{}{1: {}})
	c.sampleRates()

	ids := map[int64]bool{}
	for _, h := range c.Snapshot().Hosts {
		ids[h.HostID] = true
	}
	if !ids[UnmatchedHostID] {
		t.Error("the unmatched bucket was forgotten")
	}
	if !ids[1] {
		t.Error("a host that still exists was forgotten")
	}
	if ids[2] {
		t.Error("a deleted host was retained")
	}
}

func TestRunFlushesOnShutdown(t *testing.T) {
	repo := &fakeRepo{}
	c := newTestCollector(repo)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx, time.Hour, 24*time.Hour) }()

	c.Record(Result{HostID: 1, Status: 200, Duration: time.Millisecond})
	cancel()
	<-done

	// The flush ticker never fired, so this row can only come from the
	// shutdown flush.
	if got := repo.written(); len(got) != 1 || got[0].Requests != 1 {
		t.Errorf("samples after shutdown = %+v, want the final interval", got)
	}
	if len(repo.pruned) == 0 {
		t.Error("retention pruning never ran")
	}
}

func TestConcurrentRecordIsSafe(t *testing.T) {
	c := newTestCollector(&fakeRepo{})

	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for range 500 {
				c.Record(Result{HostID: int64(g % 4), Status: 200, Duration: time.Millisecond})
				c.RequestStarted(int64(g % 4))
				c.RequestFinished(int64(g % 4))
			}
		}(g)
	}
	// Sample and read concurrently: the collector's ticker runs while
	// several clients read, which is exactly the production arrangement.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 50 {
			c.sampleRates()
			c.Snapshot()
			c.Snapshot()
		}
	}()
	wg.Wait()

	snap := c.Snapshot()
	for _, h := range snap.Hosts {
		if h.Traffic.ActiveConns != 0 {
			t.Errorf("host %d left %d connections active", h.HostID, h.Traffic.ActiveConns)
		}
	}
}
