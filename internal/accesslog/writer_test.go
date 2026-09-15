package accesslog

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

type fakeRepo struct {
	mu      sync.Mutex
	entries []domain.AccessLogEntry
	pruned  int
	// block holds Write until released, to simulate a slow database.
	block chan struct{}
	err   error
}

func (f *fakeRepo) Write(_ context.Context, batch []domain.AccessLogEntry) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.entries = append(f.entries, batch...)
	return nil
}

func (f *fakeRepo) Query(context.Context, domain.AccessLogQuery) ([]domain.AccessLogEntry, int, error) {
	return nil, 0, nil
}

func (f *fakeRepo) Prune(context.Context, time.Time, int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned++
	return 0, nil
}

func (f *fakeRepo) written() []domain.AccessLogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.AccessLogEntry(nil), f.entries...)
}

func newTestWriter(repo domain.AccessLogRepository) *Writer {
	return New(repo, Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func entry(path string) domain.AccessLogEntry {
	return domain.AccessLogEntry{
		Timestamp: time.Now(), HostID: 1, Method: "GET",
		Path: path, Status: 200, ClientIP: "203.0.113.9",
	}
}

func TestWriterPersistsInBatches(t *testing.T) {
	repo := &fakeRepo{}
	w := newTestWriter(repo)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()

	for i := range 10 {
		w.Record(entry("/p" + string(rune('0'+i))))
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(repo.written()) < 10 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if got := len(repo.written()); got != 10 {
		t.Fatalf("persisted %d entries, want 10", got)
	}
	if stats := w.Stats(); stats.Written != 10 || stats.Dropped != 0 {
		t.Errorf("stats = %+v, want 10 written and none dropped", stats)
	}
}

// TestRecordNeverBlocks is the whole point of the design: a database that
// cannot keep up must cost the proxy nothing.
func TestRecordNeverBlocks(t *testing.T) {
	repo := &fakeRepo{block: make(chan struct{})}
	w := newTestWriter(repo)
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)

	// Far more than the buffer holds, against a repository that is wedged.
	start := time.Now()
	for range bufferSize * 3 {
		w.Record(entry("/busy"))
	}
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("recording took %v with a stalled database; it must not block", elapsed)
	}
	if dropped := w.Stats().Dropped; dropped == 0 {
		t.Error("nothing was reported as dropped, so the overflow went unnoticed")
	}

	close(repo.block)
	cancel()
}

func TestShutdownWritesWhatIsQueued(t *testing.T) {
	repo := &fakeRepo{}
	w := newTestWriter(repo)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()

	// Fewer than a batch and sooner than the flush tick, so only the
	// shutdown drain can persist these.
	for range 5 {
		w.Record(entry("/last"))
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if got := len(repo.written()); got != 5 {
		t.Errorf("persisted %d entries at shutdown, want 5", got)
	}
}

func TestOversizedFieldsAreTruncated(t *testing.T) {
	repo := &fakeRepo{}
	w := newTestWriter(repo)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()

	long := make([]byte, 10_000)
	for i := range long {
		long[i] = 'a'
	}
	e := entry(string(long))
	e.UserAgent = string(long)
	w.Record(e)

	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	got := repo.written()
	if len(got) != 1 {
		t.Fatalf("persisted %d entries, want 1", len(got))
	}
	// A client controls both fields; without a cap either could be
	// megabytes per request.
	if len(got[0].Path) > 600 || len(got[0].UserAgent) > 600 {
		t.Errorf("path %d bytes, user agent %d bytes — both should be clamped",
			len(got[0].Path), len(got[0].UserAgent))
	}
}

func TestWriteFailuresAreCountedNotFatal(t *testing.T) {
	repo := &fakeRepo{err: context.DeadlineExceeded}
	w := newTestWriter(repo)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()

	for range 3 {
		w.Record(entry("/x"))
	}
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	if stats := w.Stats(); stats.Dropped == 0 {
		t.Error("a failing repository did not report dropped entries")
	}
}

func TestRunPrunesOnStart(t *testing.T) {
	repo := &fakeRepo{}
	w := newTestWriter(repo)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.pruned == 0 {
		t.Error("retention never ran")
	}
}

func TestConcurrentRecordIsSafe(t *testing.T) {
	repo := &fakeRepo{}
	w := newTestWriter(repo)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()

	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for range 200 {
				w.Record(entry("/concurrent"))
				w.Stats()
			}
		}(g)
	}
	wg.Wait()
	cancel()
	<-done
}
