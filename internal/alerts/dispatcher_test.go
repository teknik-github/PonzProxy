package alerts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

type fakeRepo struct {
	mu         sync.Mutex
	channels   []domain.AlertChannel
	deliveries []string
}

func (f *fakeRepo) List(context.Context) ([]domain.AlertChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.AlertChannel(nil), f.channels...), nil
}

func (f *fakeRepo) Get(_ context.Context, id int64) (*domain.AlertChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.channels {
		if f.channels[i].ID == id {
			c := f.channels[i]
			return &c, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeRepo) Create(context.Context, *domain.AlertChannel) error { return nil }
func (f *fakeRepo) Update(context.Context, *domain.AlertChannel) error { return nil }
func (f *fakeRepo) Delete(context.Context, int64) error                { return nil }

func (f *fakeRepo) RecordDelivery(_ context.Context, _ int64, _ time.Time, deliveryErr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries = append(f.deliveries, deliveryErr)
	return nil
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// receiver is a stand-in webhook that records what it was sent.
type receiver struct {
	*httptest.Server
	mu       sync.Mutex
	payloads []Payload
	status   int
	block    chan struct{}
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{status: http.StatusOK}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.block != nil {
			<-r.block
		}
		var p Payload
		_ = json.NewDecoder(req.Body).Decode(&p)
		r.mu.Lock()
		r.payloads = append(r.payloads, p)
		status := r.status
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) received() []Payload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Payload(nil), r.payloads...)
}

func channel(id int64, url string, events ...domain.AlertEvent) domain.AlertChannel {
	c := domain.AlertChannel{
		ID: id, Name: "chat", Type: domain.AlertWebhook, URL: url,
		Enabled: true, Events: events, MinInterval: time.Minute,
	}
	c.Normalize()
	return c
}

func runDispatcher(t *testing.T, repo domain.AlertChannelRepository) (*Dispatcher, context.CancelFunc) {
	t.Helper()
	d := New(repo, discard())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return d, cancel
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAlertIsDeliveredToSubscribedChannels(t *testing.T) {
	wanted := newReceiver(t)
	other := newReceiver(t)

	repo := &fakeRepo{channels: []domain.AlertChannel{
		channel(1, wanted.URL, domain.AlertUpstreamDown),
		// Subscribed to a different event, so it must stay silent.
		channel(2, other.URL, domain.AlertCertificateExpiring),
	}}
	d, _ := runDispatcher(t, repo)

	UpstreamDown(d, "api.example.com", "10.0.0.1:8080", "connection refused")

	waitFor(t, "delivery", func() bool { return len(wanted.received()) == 1 })
	if n := len(other.received()); n != 0 {
		t.Errorf("a channel subscribed to a different event got %d alerts", n)
	}

	got := wanted.received()[0]
	if got.Event != domain.AlertUpstreamDown {
		t.Errorf("event = %q", got.Event)
	}
	if got.Severity != "critical" {
		t.Errorf("severity = %q, want critical", got.Severity)
	}
	// The message must name the host and the backend, or the reader has to
	// go and look anyway.
	if got.Host != "api.example.com" || got.Upstream != "10.0.0.1:8080" {
		t.Errorf("payload does not identify what broke: %+v", got)
	}
	if got.Text == "" {
		t.Error("text is empty; a chat client would render nothing")
	}
}

// TestFlappingIsThrottled is the reason MinInterval exists: an alert stream
// nobody can read is worse than none.
func TestFlappingIsThrottled(t *testing.T) {
	rec := newReceiver(t)
	repo := &fakeRepo{channels: []domain.AlertChannel{
		channel(1, rec.URL, domain.AlertUpstreamDown),
	}}
	d, _ := runDispatcher(t, repo)

	for range 20 {
		UpstreamDown(d, "api.example.com", "10.0.0.1:8080", "connection refused")
	}
	waitFor(t, "the first delivery", func() bool { return len(rec.received()) >= 1 })
	time.Sleep(300 * time.Millisecond)

	if n := len(rec.received()); n != 1 {
		t.Errorf("delivered %d messages for one flapping backend, want 1", n)
	}
}

// TestThrottleIsPerSubject guards the detail that matters during an incident:
// one noisy backend must not silence alerts about a different one.
func TestThrottleIsPerSubject(t *testing.T) {
	rec := newReceiver(t)
	repo := &fakeRepo{channels: []domain.AlertChannel{
		channel(1, rec.URL, domain.AlertUpstreamDown),
	}}
	d, _ := runDispatcher(t, repo)

	for range 5 {
		UpstreamDown(d, "api.example.com", "10.0.0.1:8080", "refused")
	}
	UpstreamDown(d, "api.example.com", "10.0.0.2:8080", "refused")
	UpstreamDown(d, "shop.example.com", "10.0.0.1:8080", "refused")

	waitFor(t, "three distinct subjects", func() bool { return len(rec.received()) == 3 })
	time.Sleep(200 * time.Millisecond)
	if n := len(rec.received()); n != 3 {
		t.Errorf("delivered %d, want exactly one per subject", n)
	}
}

func TestDisabledChannelIsSkipped(t *testing.T) {
	rec := newReceiver(t)
	c := channel(1, rec.URL, domain.AlertUpstreamDown)
	c.Enabled = false
	repo := &fakeRepo{channels: []domain.AlertChannel{c}}
	d, _ := runDispatcher(t, repo)

	UpstreamDown(d, "api.example.com", "10.0.0.1:8080", "refused")
	time.Sleep(300 * time.Millisecond)
	if n := len(rec.received()); n != 0 {
		t.Errorf("a disabled channel received %d alerts", n)
	}
}

// TestRaiseNeverBlocks is the other half of the contract: the health checker
// and the request path both call Raise, and neither may wait on a webhook.
func TestRaiseNeverBlocks(t *testing.T) {
	rec := newReceiver(t)
	rec.block = make(chan struct{})
	repo := &fakeRepo{channels: []domain.AlertChannel{
		channel(1, rec.URL, domain.AlertUpstreamDown),
	}}
	d, _ := runDispatcher(t, repo)

	start := time.Now()
	for i := range queueSize * 3 {
		UpstreamDown(d, "api.example.com", "backend-"+string(rune('a'+i%26)), "refused")
	}
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("raising took %v against a stalled webhook; it must not block", elapsed)
	}
	if d.Stats().Dropped == 0 {
		t.Error("the overflow was not reported as dropped")
	}
	close(rec.block)
}

func TestFailedDeliveryIsRecordedNotFatal(t *testing.T) {
	rec := newReceiver(t)
	rec.status = http.StatusInternalServerError
	repo := &fakeRepo{channels: []domain.AlertChannel{
		channel(1, rec.URL, domain.AlertUpstreamDown),
	}}
	d, _ := runDispatcher(t, repo)

	UpstreamDown(d, "api.example.com", "10.0.0.1:8080", "refused")

	waitFor(t, "the failure to be recorded", func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return len(repo.deliveries) > 0 && repo.deliveries[len(repo.deliveries)-1] != ""
	})
	if d.Stats().Failed == 0 {
		t.Error("a rejected delivery was not counted as failed")
	}
	// A 500 is retried once, so the endpoint should have seen two attempts.
	if n := len(rec.received()); n < 2 {
		t.Errorf("endpoint saw %d attempts, want a retry", n)
	}
}

func TestTestDeliveryReportsTheOutcome(t *testing.T) {
	rec := newReceiver(t)
	repo := &fakeRepo{}
	d := New(repo, discard())

	c := channel(1, rec.URL, domain.AlertUpstreamDown)
	if err := d.Test(context.Background(), &c); err != nil {
		t.Fatalf("test delivery failed: %v", err)
	}
	if n := len(rec.received()); n != 1 {
		t.Fatalf("endpoint received %d, want 1", n)
	}

	// A broken endpoint must surface as an error, not a silent success.
	rec.status = http.StatusBadGateway
	if err := d.Test(context.Background(), &c); err == nil {
		t.Error("a rejecting endpoint reported success")
	}
}

func TestConcurrentRaiseIsSafe(t *testing.T) {
	rec := newReceiver(t)
	repo := &fakeRepo{channels: []domain.AlertChannel{
		channel(1, rec.URL, domain.AlertUpstreamDown, domain.AlertUpstreamRecovered),
	}}
	d, _ := runDispatcher(t, repo)

	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for range 100 {
				UpstreamDown(d, "api", "backend-"+string(rune('a'+g)), "refused")
				UpstreamRecovered(d, "api", "backend-"+string(rune('a'+g)))
				d.Stats()
			}
		}(g)
	}
	wg.Wait()
}

func TestNilRaiserIsSafe(t *testing.T) {
	// Every call site passes an optional raiser; a nil one must simply do
	// nothing rather than panic on a proxy that has no alerting.
	UpstreamDown(nil, "api", "backend", "refused")
	UpstreamRecovered(nil, "api", "backend")
	UpstreamEjected(nil, "api", "backend", time.Minute, "refused")
	HostUnavailable(nil, "api", 3)
	CertificateExpiring(nil, "cert", 5)
	CertificateFailed(nil, "cert", "dns error")
}
