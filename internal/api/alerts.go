package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/alerts"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// alertChannelView is what the API returns. The webhook URL is replaced by a
// masked form: for a chat webhook the URL is itself the credential, and anyone
// who can read the console should not thereby be able to post as this
// installation.
type alertChannelView struct {
	domain.AlertChannel
	URL string `json:"url"`
	// MinIntervalSeconds mirrors MinInterval in the unit the form uses.
	MinIntervalSeconds int `json:"minIntervalSeconds"`
}

func viewOfChannel(c domain.AlertChannel) alertChannelView {
	return alertChannelView{
		AlertChannel:       c,
		URL:                c.MaskedURL(),
		MinIntervalSeconds: int(c.MinInterval.Seconds()),
	}
}

type alertChannelPayload struct {
	Name    string                  `json:"name"`
	Type    domain.AlertChannelType `json:"type"`
	Enabled bool                    `json:"enabled"`
	// URL may be empty on an update, which keeps the stored one. That is
	// the only way to edit a channel without retyping a secret the console
	// never showed you.
	URL                string              `json:"url"`
	Events             []domain.AlertEvent `json:"events"`
	MinIntervalSeconds int                 `json:"minIntervalSeconds"`
}

func (p alertChannelPayload) toDomain() domain.AlertChannel {
	return domain.AlertChannel{
		Name:        p.Name,
		Type:        p.Type,
		Enabled:     p.Enabled,
		URL:         p.URL,
		Events:      p.Events,
		MinInterval: secondsToDuration(p.MinIntervalSeconds),
	}
}

func (s *Server) handleListAlertChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.opts.AlertChannels.List(r.Context())
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	views := make([]alertChannelView, 0, len(channels))
	for _, c := range channels {
		views = append(views, viewOfChannel(c))
	}
	writeJSON(w, s.logger, http.StatusOK, views)
}

// handleListAlertEvents describes the events from the server, so the form is
// built from what this version actually supports.
func (s *Server) handleListAlertEvents(w http.ResponseWriter, _ *http.Request) {
	type entry struct {
		Value       domain.AlertEvent `json:"value"`
		Label       string            `json:"label"`
		Severity    string            `json:"severity"`
		Description string            `json:"description"`
	}
	descriptions := map[domain.AlertEvent]struct{ label, detail string }{
		domain.AlertUpstreamDown:        {"Upstream down", "A health check stopped passing and the backend was taken out of rotation."},
		domain.AlertUpstreamRecovered:   {"Upstream recovered", "It started answering again. Useful for closing an incident without checking."},
		domain.AlertUpstreamEjected:     {"Upstream ejected", "Real requests could not connect, so it was removed without waiting for a probe."},
		domain.AlertHostUnavailable:     {"Host has nothing left", "Every upstream is out. Visitors are seeing errors right now."},
		domain.AlertCertificateExpiring: {"Certificate expiring", "Renewal has not produced a new certificate yet and time is running out."},
		domain.AlertCertificateFailed:   {"Certificate renewal failed", "The only warning before a certificate simply expires."},
	}

	out := make([]entry, 0, len(domain.AlertEvents()))
	for _, e := range domain.AlertEvents() {
		d := descriptions[e]
		out = append(out, entry{Value: e, Label: d.label, Severity: e.Severity(), Description: d.detail})
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *Server) handleCreateAlertChannel(w http.ResponseWriter, r *http.Request) {
	var payload alertChannelPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, s.logger, err)
		return
	}

	channel := payload.toDomain()
	channel.Normalize()
	if err := channel.Validate(); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.AlertChannels.Create(r.Context(), &channel); err != nil {
		writeError(w, s.logger, err)
		return
	}
	s.reloadAlerts(r)

	s.logger.Info("alert channel created", "name", channel.Name, "events", len(channel.Events))
	writeJSON(w, s.logger, http.StatusCreated, viewOfChannel(channel))
}

func (s *Server) handleUpdateAlertChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	var payload alertChannelPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, s.logger, err)
		return
	}

	existing, err := s.opts.AlertChannels.Get(r.Context(), id)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	channel := payload.toDomain()
	channel.ID = id
	if channel.URL == "" {
		// The console never showed the URL, so an empty field means "leave
		// it alone" rather than "clear it".
		channel.URL = existing.URL
	}
	channel.Normalize()
	if err := channel.Validate(); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.AlertChannels.Update(r.Context(), &channel); err != nil {
		writeError(w, s.logger, err)
		return
	}
	s.reloadAlerts(r)

	s.logger.Info("alert channel updated", "name", channel.Name)
	writeJSON(w, s.logger, http.StatusOK, viewOfChannel(channel))
}

func (s *Server) handleDeleteAlertChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.AlertChannels.Delete(r.Context(), id); err != nil {
		writeError(w, s.logger, err)
		return
	}
	s.reloadAlerts(r)

	s.logger.Info("alert channel deleted", "id", id)
	writeJSON(w, s.logger, http.StatusNoContent, nil)
}

// handleTestAlertChannel posts a sample alert and waits for the result, so an
// operator learns whether a webhook they just pasted actually works rather
// than finding out during an incident.
func (s *Server) handleTestAlertChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if s.opts.AlertTester == nil {
		writeError(w, s.logger, errors.Join(errBadRequest,
			errors.New("alert delivery is not running")))
		return
	}

	channel, err := s.opts.AlertChannels.Get(r.Context(), id)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	if err := s.opts.AlertTester(ctx, channel); err != nil {
		// A failed test is information, not a server fault: report it as
		// the outcome rather than a 500.
		writeJSON(w, s.logger, http.StatusOK, map[string]any{
			"delivered": false,
			"error":     err.Error(),
		})
		return
	}
	writeJSON(w, s.logger, http.StatusOK, map[string]any{"delivered": true})
}

// reloadAlerts republishes the channel list to the dispatcher so an edit takes
// effect at once rather than at its next poll.
func (s *Server) reloadAlerts(r *http.Request) {
	if s.opts.ReloadAlerts == nil {
		return
	}
	if err := s.opts.ReloadAlerts(r.Context()); err != nil {
		s.logger.Error("reload alert channels", "error", err)
	}
}

// alertStatsResponse lets the console show that alerts are being dropped or
// rejected, which is otherwise indistinguishable from nothing happening.
func (s *Server) handleAlertStats(w http.ResponseWriter, _ *http.Request) {
	var stats alerts.Stats
	if s.opts.AlertStats != nil {
		stats = s.opts.AlertStats()
	}
	writeJSON(w, s.logger, http.StatusOK, stats)
}
