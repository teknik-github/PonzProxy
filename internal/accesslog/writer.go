// Package accesslog records proxied requests for later search.
//
// The whole design follows from one rule: the request path must never wait on
// the log. Entries are handed to a buffered channel and written in batches by
// a background goroutine; when the buffer is full an entry is dropped rather
// than blocking a request, and the drop is counted so the console can say so
// instead of quietly showing an incomplete history.
package accesslog

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/platform/logging"
)

const (
	// bufferSize is how many entries may be waiting to be written. At a few
	// thousand it absorbs a burst without letting the backlog grow into
	// meaningful memory.
	bufferSize = 4096
	// batchSize bounds one transaction.
	batchSize = 256
	// flushInterval writes a partial batch so a quiet host's entries still
	// appear promptly.
	flushInterval = 2 * time.Second
)

// Writer buffers entries and persists them in batches.
type Writer struct {
	repo   domain.AccessLogRepository
	logger *slog.Logger

	entries chan domain.AccessLogEntry
	dropped atomic.Uint64
	written atomic.Uint64

	retention time.Duration
	maxRows   int
}

// Options configures retention. Zero values take the defaults.
type Options struct {
	Retention time.Duration
	MaxRows   int
}

// New builds a writer. Run must be called for anything to be persisted.
func New(repo domain.AccessLogRepository, opts Options, logger *slog.Logger) *Writer {
	if opts.Retention <= 0 {
		opts.Retention = 7 * 24 * time.Hour
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = 500_000
	}
	return &Writer{
		repo:      repo,
		logger:    logger.With("component", "accesslog"),
		entries:   make(chan domain.AccessLogEntry, bufferSize),
		retention: opts.Retention,
		maxRows:   opts.MaxRows,
	}
}

// Record queues one request. It never blocks: a full buffer means the writer
// cannot keep up, and slowing the proxy down to record its own traffic would
// be the wrong trade every time.
func (w *Writer) Record(e domain.AccessLogEntry) {
	e.Truncate()
	select {
	case w.entries <- e:
	default:
		w.dropped.Add(1)
	}
}

// Stats reports what the writer has done, so the console can show that a
// history is incomplete rather than letting it look whole.
type Stats struct {
	Written uint64 `json:"written"`
	Dropped uint64 `json:"dropped"`
	Pending int    `json:"pending"`
}

// Stats returns the current counters.
func (w *Writer) Stats() Stats {
	return Stats{
		Written: w.written.Load(),
		Dropped: w.dropped.Load(),
		Pending: len(w.entries),
	}
}

// Run drains the buffer until ctx is cancelled, then writes whatever is left
// so a shutdown does not lose the last few seconds.
func (w *Writer) Run(ctx context.Context) {
	flush := time.NewTicker(flushInterval)
	defer flush.Stop()

	// Retention runs far less often than writing; hourly is ample for a
	// window measured in days.
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()

	batch := make([]domain.AccessLogEntry, 0, batchSize)
	w.prune(ctx)

	for {
		select {
		case <-ctx.Done():
			batch = append(batch, w.drain()...)
			// The context is already cancelled, so the final write needs a
			// deadline of its own or it would be refused before it starts.
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			w.flush(final, batch)
			cancel()

			if dropped := w.dropped.Load(); dropped > 0 {
				w.logger.Warn("access log entries were dropped", "dropped", dropped)
			}
			return

		case e := <-w.entries:
			batch = append(batch, e)
			if len(batch) >= batchSize {
				w.flush(ctx, batch)
				batch = batch[:0]
			}

		case <-flush.C:
			if len(batch) > 0 {
				w.flush(ctx, batch)
				batch = batch[:0]
			}

		case <-prune.C:
			w.prune(ctx)
		}
	}
}

// drain empties the queue without blocking, for the final write at shutdown.
func (w *Writer) drain() []domain.AccessLogEntry {
	var out []domain.AccessLogEntry
	for {
		select {
		case e := <-w.entries:
			out = append(out, e)
		default:
			return out
		}
	}
}

func (w *Writer) flush(ctx context.Context, batch []domain.AccessLogEntry) {
	if len(batch) == 0 {
		return
	}
	if err := w.repo.Write(ctx, batch); err != nil {
		// Losing log rows degrades a search; it must never take the proxy
		// down, so this is reported and counted as dropped.
		w.logger.Error("write access log batch", "error", err, "entries", len(batch))
		w.dropped.Add(uint64(len(batch)))
		return
	}
	w.written.Add(uint64(len(batch)))
}

func (w *Writer) prune(ctx context.Context) {
	n, err := w.repo.Prune(ctx, time.Now().Add(-w.retention), w.maxRows)
	if err != nil {
		if !logging.IsShutdown(err) {
			w.logger.Error("prune access log", "error", err)
		}
		return
	}
	if n > 0 {
		w.logger.Info("pruned access log", "rows", n,
			"retention", w.retention, "maxRows", w.maxRows)
	}
}
