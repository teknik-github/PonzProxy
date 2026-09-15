// Package alerts delivers notable events to operator-configured webhooks.
//
// Two rules shape the whole design. First, nothing that raises an alert may
// wait on one being delivered: the health checker and the request path both
// call in, and a webhook that hangs must not stall either. Second, a flapping
// backend must not become hundreds of messages — an alert stream nobody can
// read is worse than none, because it teaches the reader to mute it.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/platform/logging"
)

const (
	// queueSize bounds how many alerts may be waiting. Alerts are rare
	// compared to requests, so a small queue is plenty; anything larger
	// would mean delivering a backlog long after it mattered.
	queueSize = 256
	// deliveryTimeout bounds one webhook attempt.
	deliveryTimeout = 10 * time.Second
	// attempts is how many times one alert is tried. Chat endpoints fail
	// transiently often enough to be worth one retry, and not so reliably
	// that a third would help.
	attempts = 2
	// retryDelay separates those attempts.
	retryDelay = 2 * time.Second
	// reloadInterval is how often the channel list is re-read, so an edit
	// in the console takes effect without a restart.
	reloadInterval = 30 * time.Second
)

// Dispatcher fans alerts out to the channels that subscribe to them.
type Dispatcher struct {
	repo   domain.AlertChannelRepository
	logger *slog.Logger
	client *http.Client

	queue   chan domain.Alert
	dropped atomic.Uint64
	sent    atomic.Uint64
	failed  atomic.Uint64

	// channels is replaced wholesale on reload, so the delivery path reads
	// a consistent snapshot without holding a lock across a webhook call.
	channels atomic.Pointer[[]domain.AlertChannel]

	// throttle remembers when each (channel, event, subject) last fired.
	throttleMu sync.Mutex
	throttle   map[throttleKey]time.Time
}

type throttleKey struct {
	channelID int64
	event     domain.AlertEvent
	subject   string
}

// New builds a dispatcher. Run must be called for anything to be delivered.
func New(repo domain.AlertChannelRepository, logger *slog.Logger) *Dispatcher {
	d := &Dispatcher{
		repo:     repo,
		logger:   logger.With("component", "alerts"),
		client:   &http.Client{Timeout: deliveryTimeout},
		queue:    make(chan domain.Alert, queueSize),
		throttle: make(map[throttleKey]time.Time),
	}
	empty := []domain.AlertChannel{}
	d.channels.Store(&empty)
	return d
}

// Raise queues an alert. It never blocks: the health checker and the request
// path both call this, and neither may wait on a webhook.
func (d *Dispatcher) Raise(a domain.Alert) {
	select {
	case d.queue <- a:
	default:
		d.dropped.Add(1)
	}
}

// Stats reports what the dispatcher has done, for the console.
type Stats struct {
	Sent    uint64 `json:"sent"`
	Failed  uint64 `json:"failed"`
	Dropped uint64 `json:"dropped"`
}

// Stats returns the current counters.
func (d *Dispatcher) Stats() Stats {
	return Stats{
		Sent:    d.sent.Load(),
		Failed:  d.failed.Load(),
		Dropped: d.dropped.Load(),
	}
}

// Reload re-reads the channel list. The API calls it after a change so an edit
// takes effect at once rather than at the next poll.
func (d *Dispatcher) Reload(ctx context.Context) error {
	channels, err := d.repo.List(ctx)
	if err != nil {
		return fmt.Errorf("load alert channels: %w", err)
	}
	d.channels.Store(&channels)
	return nil
}

// Run delivers queued alerts until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	if err := d.Reload(ctx); err != nil && !logging.IsShutdown(err) {
		d.logger.Error("load alert channels", "error", err)
	}

	reload := time.NewTicker(reloadInterval)
	defer reload.Stop()

	// Throttle entries are only useful for as long as the longest interval,
	// so they are swept rather than kept for the life of the process.
	sweep := time.NewTicker(time.Hour)
	defer sweep.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case a := <-d.queue:
			d.deliver(ctx, a)
		case <-reload.C:
			if err := d.Reload(ctx); err != nil && !logging.IsShutdown(err) {
				d.logger.Error("reload alert channels", "error", err)
			}
		case <-sweep.C:
			d.sweepThrottle()
		}
	}
}

// deliver sends one alert to every channel that wants it and is not throttled.
func (d *Dispatcher) deliver(ctx context.Context, a domain.Alert) {
	channels := *d.channels.Load()
	now := time.Now()

	for i := range channels {
		c := &channels[i]
		if !c.Wants(a.Event) {
			continue
		}
		if d.throttled(c, a, now) {
			continue
		}
		d.post(ctx, c, a)
	}
}

// throttled reports whether this channel has already been told about this
// subject recently, and records the send when it has not.
//
// The key includes the subject so one flapping backend cannot suppress alerts
// about a different one — which is exactly the alert an operator most needs
// during an incident.
func (d *Dispatcher) throttled(c *domain.AlertChannel, a domain.Alert, now time.Time) bool {
	key := throttleKey{channelID: c.ID, event: a.Event, subject: a.Subject}

	d.throttleMu.Lock()
	defer d.throttleMu.Unlock()

	if last, found := d.throttle[key]; found && now.Sub(last) < c.MinInterval {
		return true
	}
	d.throttle[key] = now
	return false
}

func (d *Dispatcher) sweepThrottle() {
	cutoff := time.Now().Add(-24 * time.Hour)

	d.throttleMu.Lock()
	defer d.throttleMu.Unlock()
	for key, at := range d.throttle {
		if at.Before(cutoff) {
			delete(d.throttle, key)
		}
	}
}

// Payload is the JSON posted to a webhook. It is deliberately flat and
// self-describing: the receiver is usually a chat integration or a small
// script, not something that can be asked to learn a schema.
type Payload struct {
	Event     domain.AlertEvent `json:"event"`
	Severity  string            `json:"severity"`
	Timestamp time.Time         `json:"timestamp"`
	Title     string            `json:"title"`
	Detail    string            `json:"detail,omitempty"`
	Host      string            `json:"host,omitempty"`
	Upstream  string            `json:"upstream,omitempty"`
	// Text repeats the title so a webhook expecting Slack's or Discord's
	// shape renders something useful with no mapping at all.
	Text   string `json:"text"`
	Source string `json:"source"`
}

func (d *Dispatcher) post(ctx context.Context, c *domain.AlertChannel, a domain.Alert) {
	body, err := json.Marshal(Payload{
		Event:     a.Event,
		Severity:  a.Severity,
		Timestamp: a.Timestamp,
		Title:     a.Title,
		Detail:    a.Detail,
		Host:      a.Host,
		Upstream:  a.Upstream,
		Text:      alertText(a),
		Source:    "ponzproxy",
	})
	if err != nil {
		d.logger.Error("encode alert", "error", err)
		return
	}

	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryDelay):
			}
		}
		if lastErr = d.attempt(ctx, c, body); lastErr == nil {
			break
		}
	}

	d.record(ctx, c, lastErr)
	if lastErr != nil {
		d.failed.Add(1)
		d.logger.Warn("alert delivery failed",
			"channel", c.Name, "event", a.Event, "error", lastErr)
		return
	}
	d.sent.Add(1)
	d.logger.Info("alert delivered", "channel", c.Name, "event", a.Event, "subject", a.Subject)
}

func (d *Dispatcher) attempt(ctx context.Context, c *domain.AlertChannel, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ponzproxy-alerts/1")

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	// The body is drained so the connection can be reused, and capped
	// because a misbehaving endpoint could otherwise stream indefinitely.
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 300 {
		return fmt.Errorf("endpoint answered %d", resp.StatusCode)
	}
	return nil
}

// record stores the outcome so the console can show a channel that has stopped
// working, rather than leaving it to be discovered when an alert never lands.
func (d *Dispatcher) record(ctx context.Context, c *domain.AlertChannel, deliveryErr error) {
	message := ""
	if deliveryErr != nil {
		message = deliveryErr.Error()
	}
	// Its own deadline: ctx may be on its way down, and the outcome is
	// worth persisting either way.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := d.repo.RecordDelivery(ctx, c.ID, time.Now().UTC(), message); err != nil {
		d.logger.Warn("record alert delivery", "channel", c.Name, "error", err)
	}
}

// Test delivers a sample alert to one channel and reports the outcome
// synchronously, so the console can tell an operator whether a webhook they
// just pasted actually works.
func (d *Dispatcher) Test(ctx context.Context, c *domain.AlertChannel) error {
	a := domain.NewAlert(domain.AlertUpstreamRecovered, "test",
		"ponzproxy test alert",
		"If you are reading this, the webhook works. No action is needed.")

	body, err := json.Marshal(Payload{
		Event: a.Event, Severity: a.Severity, Timestamp: a.Timestamp,
		Title: a.Title, Detail: a.Detail, Text: alertText(a), Source: "ponzproxy",
	})
	if err != nil {
		return err
	}

	deliveryErr := d.attempt(ctx, c, body)
	d.record(ctx, c, deliveryErr)
	return deliveryErr
}

// alertText renders the single line a chat client will show.
func alertText(a domain.Alert) string {
	text := a.Title
	if a.Detail != "" {
		text += " — " + a.Detail
	}
	return text
}
