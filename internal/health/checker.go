// Package health actively probes upstreams and flips them in and out of
// rotation, so a dead backend stops receiving traffic before a client ever
// sees the failure.
package health

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/alerts"
	"github.com/ponzproxy/ponzproxy/internal/balancer"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// Target is one host's pool plus the check settings that apply to it.
type Target struct {
	HostID int64
	Name   string
	Pool   *balancer.Pool
	Check  domain.HealthCheck
}

// StateChangeFunc is called whenever a backend transitions between healthy and
// unhealthy, so the control plane can push the change to connected UIs.
type StateChangeFunc func(hostID int64, backend *balancer.Backend, healthy bool)

// Checker supervises one probing goroutine per host. Sync replaces the whole
// set of targets, which is how a configuration reload reaches it.
type Checker struct {
	logger   *slog.Logger
	onChange StateChangeFunc
	// alerts is optional: nothing here waits on a delivery, and a nil
	// raiser simply means no one asked to be told.
	alerts alerts.Raiser

	mu      sync.Mutex
	running map[int64]*hostChecker
	closed  bool
}

// SetAlerts wires in the alert dispatcher. It is set after construction
// because the dispatcher and the checker are built in either order.
func (c *Checker) SetAlerts(r alerts.Raiser) { c.alerts = r }

// New builds a checker. onChange may be nil when no one is listening.
func New(logger *slog.Logger, onChange StateChangeFunc) *Checker {
	if onChange == nil {
		onChange = func(int64, *balancer.Backend, bool) {}
	}
	return &Checker{
		logger:   logger.With("component", "health"),
		onChange: onChange,
		running:  make(map[int64]*hostChecker),
	}
}

// Sync makes the running probes match targets exactly: it starts checkers for
// new hosts, stops them for removed ones, and restarts any whose pool or
// settings changed.
func (c *Checker) Sync(targets []Target) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}

	wanted := make(map[int64]struct{}, len(targets))
	for _, t := range targets {
		wanted[t.HostID] = struct{}{}

		existing, ok := c.running[t.HostID]
		if ok && existing.matches(t) {
			continue // nothing about this host's probing changed
		}
		if ok {
			existing.stop()
		}
		if !t.Check.Enabled || len(t.Pool.Backends()) == 0 {
			delete(c.running, t.HostID)
			continue
		}
		c.running[t.HostID] = c.start(t)
	}

	for hostID, hc := range c.running {
		if _, keep := wanted[hostID]; !keep {
			hc.stop()
			delete(c.running, hostID)
		}
	}
}

// Close stops every probe and blocks until they have exited.
func (c *Checker) Close() {
	c.mu.Lock()
	c.closed = true
	running := c.running
	c.running = make(map[int64]*hostChecker)
	c.mu.Unlock()

	for _, hc := range running {
		hc.stop()
	}
}

func (c *Checker) start(t Target) *hostChecker {
	ctx, cancel := context.WithCancel(context.Background())
	hc := &hostChecker{
		target: t,
		cancel: cancel,
		done:   make(chan struct{}),
		client: newProbeClient(t.Check.Timeout),
		logger: c.logger.With("host", t.Name, "hostId", t.HostID),
		notify: c.onChange,
		alerts: c.alerts,
	}
	go hc.run(ctx)
	return hc
}

// hostChecker probes every backend of one host on a fixed interval.
type hostChecker struct {
	target Target
	alerts alerts.Raiser
	cancel context.CancelFunc
	done   chan struct{}
	client *http.Client
	logger *slog.Logger
	notify StateChangeFunc
}

// matches reports whether a target is identical to the one already running, so
// an unrelated configuration edit does not restart healthy probes.
func (h *hostChecker) matches(t Target) bool {
	return h.target.Pool == t.Pool && h.target.Check == t.Check
}

func (h *hostChecker) stop() {
	h.cancel()
	<-h.done
}

func (h *hostChecker) run(ctx context.Context) {
	defer close(h.done)
	defer h.client.CloseIdleConnections()

	ticker := time.NewTicker(h.target.Check.Interval)
	defer ticker.Stop()

	// Probe once immediately so a newly configured host is verified without
	// waiting out a full interval.
	h.probeAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.probeAll(ctx)
		}
	}
}

// probeAll checks every backend in parallel and waits for the round to finish,
// so probes never stack up if a backend is slower than the interval.
func (h *hostChecker) probeAll(ctx context.Context) {
	backends := h.target.Pool.Backends()

	var wg sync.WaitGroup
	for _, b := range backends {
		if !b.Upstream.Enabled {
			continue
		}
		wg.Add(1)
		go func(b *balancer.Backend) {
			defer wg.Done()
			h.probe(ctx, b)
		}(b)
	}
	wg.Wait()
}

func (h *hostChecker) probe(ctx context.Context, b *balancer.Backend) {
	ctx, cancel := context.WithTimeout(ctx, h.target.Check.Timeout)
	defer cancel()

	err := h.doProbe(ctx, b)
	if err != nil {
		// A cancelled probe means we are shutting down or reloading, not
		// that the backend is unhealthy. Counting it would take healthy
		// backends out of rotation on every configuration change.
		if ctx.Err() != nil && context.Cause(ctx) == context.Canceled {
			return
		}
		b.RecordError(err)
	}

	if changed := b.RecordProbe(err == nil,
		h.target.Check.HealthyThreshold, h.target.Check.UnhealthyThreshold); changed {
		healthy := err == nil
		if healthy {
			// A backend that answers a probe again should not sit out the
			// rest of a passive ejection window it earned earlier.
			b.ClearEjection()
			h.logger.Info("upstream recovered", "upstream", b.Key())
			alerts.UpstreamRecovered(h.alerts, h.target.Name, b.Key())
		} else {
			h.logger.Warn("upstream marked down", "upstream", b.Key(), "error", err)
			alerts.UpstreamDown(h.alerts, h.target.Name, b.Key(), errString(err))

			// Losing the last one is a different event: it is the moment
			// visitors start seeing errors, not merely a backend going
			// away, and an operator needs to be able to alert on it
			// separately.
			if up, total := h.target.Pool.HealthyCount(); up == 0 && total > 0 {
				alerts.HostUnavailable(h.alerts, h.target.Name, total)
			}
		}
		h.notify(h.target.HostID, b, healthy)
	}
}

func (h *hostChecker) doProbe(ctx context.Context, b *balancer.Backend) error {
	url := b.URL.String() + h.target.Check.Path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	req.Header.Set("User-Agent", "ponzproxy-health/1")

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	// The body is drained and closed so the connection returns to the pool
	// instead of being torn down on every probe.
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if want := h.target.Check.ExpectStatus; want != 0 {
		if resp.StatusCode != want {
			return fmt.Errorf("status %d, want %d", resp.StatusCode, want)
		}
		return nil
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// errString renders a probe failure for an alert, tolerating a nil error.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// newProbeClient builds a client dedicated to health checks. Redirects are not
// followed: a backend that redirects its health endpoint elsewhere is not
// proof that the backend itself is serving.
func newProbeClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: timeout}).DialContext,
			TLSHandshakeTimeout: timeout,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     90 * time.Second,
			// Health checks target internal backends, which routinely use
			// self-signed certificates. Verification here would report a
			// working backend as down; real trust decisions belong to the
			// proxy transport, which honours SkipTLSVerify per upstream.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}
