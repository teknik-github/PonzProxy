package alerts

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

/* --------------------------------------------------------------- fakes -- */

type usageHosts struct {
	mu    sync.Mutex
	hosts []domain.Host
}

func (u *usageHosts) List(context.Context) ([]domain.Host, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]domain.Host(nil), u.hosts...), nil
}

type usageMetrics struct {
	mu    sync.Mutex
	rows  []domain.UsageRow
	calls int
	// window records the span of the last query, so a test can prove the
	// budget is measured over the period the host asked for.
	window time.Duration
}

func (m *usageMetrics) WriteSamples(context.Context, []domain.Sample) error { return nil }
func (m *usageMetrics) Query(context.Context, domain.MetricsQuery) ([]domain.Sample, error) {
	return nil, nil
}
func (m *usageMetrics) Prune(context.Context, time.Time) (int64, error) { return 0, nil }
func (m *usageMetrics) Usage(_ context.Context, from, to time.Time) ([]domain.UsageRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.window = to.Sub(from)
	return append([]domain.UsageRow(nil), m.rows...), nil
}

type capturedAlerts struct {
	mu     sync.Mutex
	raised []domain.Alert
}

func (c *capturedAlerts) Raise(a domain.Alert) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raised = append(c.raised, a)
}
func (c *capturedAlerts) all() []domain.Alert {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]domain.Alert(nil), c.raised...)
}

func budgetHost(id int64, name string, bytes int64, days int, on bool) domain.Host {
	h := domain.Host{
		ID: id, Name: name, Enabled: true,
		UsageAlert: domain.UsageAlert{Enabled: on, Bytes: bytes, PeriodDays: days},
	}
	h.UsageAlert.Normalize()
	return h
}

func newWatcher(hosts *usageHosts, metrics *usageMetrics, out *capturedAlerts) *UsageWatcher {
	return NewUsageWatcher(hosts, metrics, out,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

/* ---------------------------------------------------------------- tests -- */

func TestOverBudgetRaisesOnceWithTheFigures(t *testing.T) {
	hosts := &usageHosts{hosts: []domain.Host{budgetHost(1, "storefront", 100<<30, 30, true)}}
	metrics := &usageMetrics{rows: []domain.UsageRow{
		{HostID: 1, BytesIn: 40 << 30, BytesOut: 80 << 30}, // 120 GiB against 100
	}}
	out := &capturedAlerts{}
	w := newWatcher(hosts, metrics, out)

	w.check(context.Background())

	raised := out.all()
	if len(raised) != 1 {
		t.Fatalf("raised %d alerts, want 1", len(raised))
	}
	a := raised[0]
	if a.Event != domain.AlertUsageExceeded {
		t.Errorf("event = %q", a.Event)
	}
	// A warning, not a critical. Waking someone at three in the morning for
	// a cost teaches them to mute the channel that carries the outages too.
	if a.Severity != "warning" {
		t.Errorf("severity = %q, want warning", a.Severity)
	}
	if a.Host != "storefront" {
		t.Errorf("host = %q", a.Host)
	}
	// The whole point is that this reaches someone who is not looking at
	// the console, so the message has to carry the numbers itself.
	for _, want := range []string{"storefront", "GB", "30 days"} {
		if !strings.Contains(a.Title, want) {
			t.Errorf("title %q does not mention %q", a.Title, want)
		}
	}
}

func TestUnderBudgetSaysNothing(t *testing.T) {
	hosts := &usageHosts{hosts: []domain.Host{budgetHost(1, "storefront", 100<<30, 30, true)}}
	metrics := &usageMetrics{rows: []domain.UsageRow{
		{HostID: 1, BytesIn: 10 << 30, BytesOut: 20 << 30},
	}}
	out := &capturedAlerts{}
	newWatcher(hosts, metrics, out).check(context.Background())

	if got := len(out.all()); got != 0 {
		t.Errorf("raised %d alerts while under budget", got)
	}
}

func TestABudgetThatIsOffIsNotChecked(t *testing.T) {
	hosts := &usageHosts{hosts: []domain.Host{budgetHost(1, "storefront", 1, 30, false)}}
	metrics := &usageMetrics{rows: []domain.UsageRow{
		{HostID: 1, BytesIn: 1 << 40, BytesOut: 1 << 40},
	}}
	out := &capturedAlerts{}
	newWatcher(hosts, metrics, out).check(context.Background())

	if got := len(out.all()); got != 0 {
		t.Errorf("raised %d alerts for a host with no budget set", got)
	}
	// And it should not have cost a query either.
	if metrics.calls != 0 {
		t.Errorf("queried usage %d times with nothing to check", metrics.calls)
	}
}

func TestStayingOverBudgetRepeatsOnlyOnceADay(t *testing.T) {
	hosts := &usageHosts{hosts: []domain.Host{budgetHost(1, "storefront", 100<<30, 30, true)}}
	metrics := &usageMetrics{rows: []domain.UsageRow{
		{HostID: 1, BytesIn: 60 << 30, BytesOut: 60 << 30},
	}}
	out := &capturedAlerts{}
	w := newWatcher(hosts, metrics, out)

	start := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	at := start
	w.now = func() time.Time { return at }

	// Being over budget stays true for days. An hourly check must not mean
	// an hourly reminder of something the operator already knows.
	for range 24 {
		w.check(context.Background())
		at = at.Add(UsageInterval)
	}
	if got := len(out.all()); got != 1 {
		t.Fatalf("raised %d alerts over 24 hourly checks, want 1", got)
	}

	// A day later it is worth saying again.
	at = start.Add(usageRepeat + time.Minute)
	w.check(context.Background())
	if got := len(out.all()); got != 2 {
		t.Errorf("raised %d alerts after a day, want 2", got)
	}
}

func TestDroppingBackUnderThenOverReportsAgainImmediately(t *testing.T) {
	hosts := &usageHosts{hosts: []domain.Host{budgetHost(1, "storefront", 100<<30, 30, true)}}
	metrics := &usageMetrics{rows: []domain.UsageRow{
		{HostID: 1, BytesIn: 60 << 30, BytesOut: 60 << 30},
	}}
	out := &capturedAlerts{}
	w := newWatcher(hosts, metrics, out)

	at := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return at }
	w.check(context.Background())

	// The rolling window moves, so a host can fall back under it.
	metrics.mu.Lock()
	metrics.rows = []domain.UsageRow{{HostID: 1, BytesIn: 1 << 30, BytesOut: 1 << 30}}
	metrics.mu.Unlock()
	at = at.Add(UsageInterval)
	w.check(context.Background())

	// Crossing again is news, not a repeat of something already said.
	metrics.mu.Lock()
	metrics.rows = []domain.UsageRow{{HostID: 1, BytesIn: 60 << 30, BytesOut: 60 << 30}}
	metrics.mu.Unlock()
	at = at.Add(UsageInterval)
	w.check(context.Background())

	if got := len(out.all()); got != 2 {
		t.Errorf("raised %d alerts, want 2: crossing again should report", got)
	}
}

func TestEachHostHasItsOwnBudget(t *testing.T) {
	hosts := &usageHosts{hosts: []domain.Host{
		budgetHost(1, "storefront", 100<<30, 30, true),
		budgetHost(2, "api", 1<<30, 30, true),
	}}
	metrics := &usageMetrics{rows: []domain.UsageRow{
		{HostID: 1, BytesIn: 1 << 30, BytesOut: 1 << 30},   // well under
		{HostID: 2, BytesIn: 10 << 30, BytesOut: 10 << 30}, // well over
	}}
	out := &capturedAlerts{}
	newWatcher(hosts, metrics, out).check(context.Background())

	raised := out.all()
	if len(raised) != 1 {
		t.Fatalf("raised %d alerts, want 1", len(raised))
	}
	if raised[0].Host != "api" {
		t.Errorf("alerted about %q, want api", raised[0].Host)
	}
}

func TestHostsSharingAWindowCostOneQuery(t *testing.T) {
	hosts := &usageHosts{hosts: []domain.Host{
		budgetHost(1, "a", 100<<30, 30, true),
		budgetHost(2, "b", 100<<30, 30, true),
		budgetHost(3, "c", 100<<30, 30, true),
		budgetHost(4, "d", 100<<30, 7, true),
	}}
	metrics := &usageMetrics{}
	newWatcher(hosts, metrics, &capturedAlerts{}).check(context.Background())

	// Three hosts on the default window must not be three scans of the
	// samples table.
	if metrics.calls != 2 {
		t.Errorf("made %d usage queries for two distinct windows, want 2", metrics.calls)
	}
}

func TestTheBudgetIsMeasuredOverTheHostsOwnWindow(t *testing.T) {
	hosts := &usageHosts{hosts: []domain.Host{budgetHost(1, "weekly", 1<<30, 7, true)}}
	metrics := &usageMetrics{}
	newWatcher(hosts, metrics, &capturedAlerts{}).check(context.Background())

	if got := metrics.window.Round(time.Hour); got != 7*24*time.Hour {
		t.Errorf("queried a window of %v, want 7 days", got)
	}
}
