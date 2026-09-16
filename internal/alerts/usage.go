package alerts

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/platform/logging"
)

// HostLister is the slice of the host repository this watcher needs, declared
// here for the same reason Raiser is: a component that only reads a list has
// no business being handed something that can delete.
type HostLister interface {
	List(ctx context.Context) ([]domain.Host, error)
}

// UsageWatcher compares each host's traffic against the budget set for it.
//
// It runs on its own slow ticker rather than on the request path. A budget is
// measured in gigabytes over weeks; checking it per request would be a
// database query per request to answer a question whose answer changes hourly.
type UsageWatcher struct {
	hosts   HostLister
	metrics domain.MetricsRepository
	raiser  Raiser
	logger  *slog.Logger
	every   time.Duration

	// lastAlert remembers when each host was last reported over budget.
	// The channel's own throttle is not enough here: being over budget
	// stays true for days or weeks, so an hourly check would deliver an
	// hourly reminder of something the operator already knows.
	mu        sync.Mutex
	lastAlert map[int64]time.Time

	// now is injectable so the tests do not have to sleep out a day.
	now func() time.Time
}

const (
	// UsageInterval is how often budgets are checked. An hour is far finer
	// than a monthly budget needs and still catches a host that runs away
	// in a morning.
	UsageInterval = time.Hour

	// usageRepeat is how often the same host may be reported over budget.
	// Once a day: the condition persists, and a reminder nobody can act on
	// any faster is the kind of noise that gets a channel muted.
	usageRepeat = 24 * time.Hour
)

func NewUsageWatcher(hosts HostLister, metrics domain.MetricsRepository,
	raiser Raiser, logger *slog.Logger) *UsageWatcher {

	return &UsageWatcher{
		hosts: hosts, metrics: metrics, raiser: raiser,
		logger:    logger.With("component", "usage"),
		every:     UsageInterval,
		lastAlert: make(map[int64]time.Time),
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// shouldAlert reports whether this host may be reported again, and records
// that it was. A host whose budget is switched off, or which drops back under,
// is forgotten so that crossing again reports immediately.
func (w *UsageWatcher) shouldAlert(hostID int64, at time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, found := w.lastAlert[hostID]; found && at.Sub(last) < usageRepeat {
		return false
	}
	w.lastAlert[hostID] = at
	return true
}

func (w *UsageWatcher) forget(hostID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.lastAlert, hostID)
}

// Run checks budgets until ctx is cancelled.
//
// The first check is deferred by one interval. A proxy restarted repeatedly
// would otherwise re-send the same "over budget" message on every start, and
// the dispatcher's suppression is keyed in memory — so it would not remember
// having sent it before the restart.
func (w *UsageWatcher) Run(ctx context.Context) {
	t := time.NewTicker(w.every)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.check(ctx)
		}
	}
}

func (w *UsageWatcher) check(ctx context.Context) {
	hosts, err := w.hosts.List(ctx)
	if err != nil {
		if !logging.IsShutdown(err) {
			w.logger.Error("could not read hosts to check their budgets", "error", err)
		}
		return
	}

	// Grouped by window so that hosts sharing one — which is every host on
	// a proxy where nobody changed the default — cost a single query.
	byWindow := make(map[int][]domain.Host, 2)
	for _, h := range hosts {
		if !h.UsageAlert.Enabled || h.UsageAlert.Bytes <= 0 {
			continue
		}
		byWindow[h.UsageAlert.PeriodDays] = append(byWindow[h.UsageAlert.PeriodDays], h)
	}
	if len(byWindow) == 0 {
		return
	}

	now := w.now()
	for days, group := range byWindow {
		rows, err := w.metrics.Usage(ctx, now.AddDate(0, 0, -days), now)
		if err != nil {
			if !logging.IsShutdown(err) {
				w.logger.Error("could not read usage", "error", err, "days", days)
			}
			continue
		}

		used := make(map[int64]int64, len(rows))
		for _, r := range rows {
			// In and out together, because that is how transfer is
			// billed and therefore what an operator budgeted for.
			used[r.HostID] = int64(r.BytesIn + r.BytesOut)
		}

		for _, h := range group {
			total := used[h.ID]
			if total < h.UsageAlert.Bytes {
				// Back under, so crossing again is news rather than a
				// repeat of something already said.
				w.forget(h.ID)
				continue
			}
			if !w.shouldAlert(h.ID, now) {
				continue
			}
			w.logger.Info("host is over its traffic budget",
				"host", h.Name, "used", total, "budget", h.UsageAlert.Bytes, "days", days)
			UsageExceeded(w.raiser, h.Name, total, h.UsageAlert.Bytes, days)
		}
	}
}
