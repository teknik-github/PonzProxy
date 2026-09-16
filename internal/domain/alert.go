package domain

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// AlertEvent is something worth telling an operator about. The set is closed
// on purpose: an alert nobody can act on is noise, and every one of these has
// an obvious next step.
type AlertEvent string

const (
	// AlertUpstreamDown fires when active probing takes a backend out.
	AlertUpstreamDown AlertEvent = "upstream_down"
	// AlertUpstreamRecovered fires when it answers again. Pairing the two
	// is what lets an operator close an incident without checking.
	AlertUpstreamRecovered AlertEvent = "upstream_recovered"
	// AlertUpstreamEjected fires when passive health removes a backend
	// after real requests failed to connect.
	AlertUpstreamEjected AlertEvent = "upstream_ejected"
	// AlertHostUnavailable fires when a host has no backend left. This is
	// the one that means visitors are seeing errors right now.
	AlertHostUnavailable AlertEvent = "host_unavailable"
	// AlertCertificateExpiring fires while there is still time to act.
	AlertCertificateExpiring AlertEvent = "certificate_expiring"
	// AlertCertificateFailed fires when a renewal attempt failed, which is
	// the only warning before a certificate simply expires.
	AlertCertificateFailed AlertEvent = "certificate_failed"
	// AlertUsageExceeded fires when a host passes the traffic budget set
	// for it. Hosting is billed by the byte, so the operator who wants
	// this wants it before the invoice, not after.
	AlertUsageExceeded AlertEvent = "usage_exceeded"
)

// AlertEvents lists every event, for the UI's picker.
func AlertEvents() []AlertEvent {
	return []AlertEvent{
		AlertUpstreamDown, AlertUpstreamRecovered, AlertUpstreamEjected,
		AlertHostUnavailable, AlertCertificateExpiring, AlertCertificateFailed,
		AlertUsageExceeded,
	}
}

func (e AlertEvent) Valid() bool {
	for _, known := range AlertEvents() {
		if e == known {
			return true
		}
	}
	return false
}

// Severity separates what needs attention now from what is merely news.
func (e AlertEvent) Severity() string {
	switch e {
	case AlertUpstreamRecovered:
		return "info"
	case AlertCertificateExpiring, AlertUsageExceeded:
		// Passing a budget costs money, not uptime. Waking someone at
		// three in the morning for it would teach them to mute the
		// channel that also carries the outages.
		return "warning"
	default:
		return "critical"
	}
}

// AlertChannelType is how an alert is delivered.
type AlertChannelType string

// AlertWebhook posts a JSON document. It is the only transport: every chat
// system and on-call tool accepts one, and adding SMTP would mean owning mail
// deliverability for an appliance that has no business doing so.
const AlertWebhook AlertChannelType = "webhook"

func (t AlertChannelType) Valid() bool { return t == AlertWebhook }

// AlertChannel is one destination and the events it wants.
type AlertChannel struct {
	ID      int64            `json:"id"`
	Name    string           `json:"name"`
	Type    AlertChannelType `json:"type"`
	Enabled bool             `json:"enabled"`

	// URL is withheld from API responses: a Slack or Discord webhook URL
	// is itself the credential, and anyone who can read the console should
	// not thereby be able to post as this installation.
	URL string `json:"-"`

	Events []AlertEvent `json:"events"`

	// MinInterval suppresses a repeat of the same event about the same
	// subject. A backend that flaps every ten seconds would otherwise
	// deliver hundreds of messages and train the reader to mute them.
	MinInterval time.Duration `json:"minInterval"`

	// LastAttempt and LastError record the most recent delivery, so a
	// channel that has silently stopped working is visible in the console
	// rather than only discovered when an alert fails to arrive.
	LastAttempt *time.Time `json:"lastAttempt,omitempty"`
	LastError   string     `json:"lastError,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Wants reports whether this channel subscribes to an event.
func (c *AlertChannel) Wants(e AlertEvent) bool {
	if !c.Enabled {
		return false
	}
	for _, subscribed := range c.Events {
		if subscribed == e {
			return true
		}
	}
	return false
}

// Normalize canonicalises operator input the way the other aggregates do.
func (c *AlertChannel) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.URL = strings.TrimSpace(c.URL)
	if c.Type == "" {
		c.Type = AlertWebhook
	}
	if c.MinInterval <= 0 {
		c.MinInterval = 5 * time.Minute
	}

	events := make([]AlertEvent, 0, len(c.Events))
	seen := make(map[AlertEvent]struct{}, len(c.Events))
	for _, e := range c.Events {
		e = AlertEvent(strings.ToLower(strings.TrimSpace(string(e))))
		if e == "" {
			continue
		}
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		events = append(events, e)
	}
	c.Events = events
}

// Validate reports every problem at once.
func (c *AlertChannel) Validate() error {
	v := &ValidationError{}

	if c.Name == "" {
		v.Add("name", "is required")
	}
	if !c.Type.Valid() {
		v.Add("type", "%q is not a supported channel type", string(c.Type))
	}
	if err := validateWebhookURL(c.URL); err != nil {
		v.Add("url", "%s", err.Error())
	}
	if len(c.Events) == 0 {
		v.Add("events", "choose at least one event, or the channel can never fire")
	}
	for _, e := range c.Events {
		if !e.Valid() {
			v.Add("events", "%q is not a known event", string(e))
		}
	}
	if c.MinInterval < time.Minute || c.MinInterval > 24*time.Hour {
		v.Add("minInterval", "must be between 1 minute and 24 hours")
	}

	return v.Err()
}

// validateWebhookURL refuses anything the dispatcher could not post to.
//
// It does not try to prevent an operator from pointing a webhook at their own
// internal network: only administrators can configure one, and reaching
// internal services is a legitimate thing to want. It does insist on an
// absolute http or https URL, so a typo fails here rather than at delivery.
func validateWebhookURL(raw string) error {
	if raw == "" {
		return errString("is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errString("is not a valid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errString("must start with http:// or https://")
	}
	if parsed.Host == "" {
		return errString("is missing a host")
	}
	return nil
}

// MaskedURL is what the console shows instead of the real one: enough to tell
// two channels apart, not enough to post with.
func (c *AlertChannel) MaskedURL() string {
	parsed, err := url.Parse(c.URL)
	if err != nil || parsed.Host == "" {
		return "—"
	}
	return parsed.Scheme + "://" + parsed.Host + "/…"
}

// Alert is one occurrence, as delivered.
type Alert struct {
	Event     AlertEvent `json:"event"`
	Severity  string     `json:"severity"`
	Timestamp time.Time  `json:"timestamp"`

	// Subject identifies what the alert is about, and is what the
	// throttle keys on: one upstream flapping must not suppress alerts
	// about a different one.
	Subject string `json:"subject"`
	// Title is a single line suitable for a chat message.
	Title string `json:"title"`
	// Detail explains what an operator should look at.
	Detail string `json:"detail,omitempty"`

	Host     string `json:"host,omitempty"`
	Upstream string `json:"upstream,omitempty"`
}

// NewAlert builds an occurrence with its severity and timestamp filled in.
func NewAlert(event AlertEvent, subject, title, detail string) Alert {
	return Alert{
		Event:     event,
		Severity:  event.Severity(),
		Timestamp: time.Now().UTC(),
		Subject:   subject,
		Title:     title,
		Detail:    detail,
	}
}

// AlertChannelRepository stores alert destinations.
type AlertChannelRepository interface {
	List(ctx context.Context) ([]AlertChannel, error)
	Get(ctx context.Context, id int64) (*AlertChannel, error)
	Create(ctx context.Context, c *AlertChannel) error
	Update(ctx context.Context, c *AlertChannel) error
	Delete(ctx context.Context, id int64) error
	// RecordDelivery stores the outcome of the most recent attempt.
	RecordDelivery(ctx context.Context, id int64, at time.Time, deliveryErr string) error
}
