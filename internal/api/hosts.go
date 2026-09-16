package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// hostPayload is the write shape of a host. It exists separately from
// domain.Host so that server-owned fields — ids, timestamps — cannot be set by
// a client, and so the wire format can evolve without touching the domain.
type hostPayload struct {
	Name             string               `json:"name"`
	Enabled          bool                 `json:"enabled"`
	Domains          []string             `json:"domains"`
	Algorithm        domain.Algorithm     `json:"algorithm"`
	Upstreams        []upstreamPayload    `json:"upstreams"`
	CertificateID    *int64               `json:"certificateId"`
	AccessListID     *int64               `json:"accessListId"`
	ForceHTTPS       bool                 `json:"forceHttps"`
	HSTSMaxAge       int                  `json:"hstsMaxAge"`
	WebSocketSupport bool                 `json:"websocketSupport"`
	PreserveHost     bool                 `json:"preserveHost"`
	HealthCheck      healthCheckPayload   `json:"healthCheck"`
	PassiveHealth    passiveHealthPayload `json:"passiveHealth"`
	AccessLog        accessLogPayload     `json:"accessLog"`
	Guardian         guardianPayload      `json:"guardian"`
	Cache            cachePayload         `json:"cache"`
	TrafficLimits    limitsPayload        `json:"trafficLimits"`
}

// limitsPayload is the per-host traffic limit. It mirrors domain.TrafficLimits
// one for one: there is no unit conversion to do, and a payload that differs
// from the type it becomes is a place for the two to drift apart.
type limitsPayload struct {
	Mode              domain.Mode `json:"mode"`
	RequestsPerSecond int         `json:"requestsPerSecond"`
	Burst             int         `json:"burst"`
	MaxConcurrent     int         `json:"maxConcurrent"`
	MaxBodyBytes      int64       `json:"maxBodyBytes"`
	Exempt            []string    `json:"exempt"`
}

// cachePayload is the per-host static asset cache. Durations are seconds, as
// the health check and passive health payloads already do.
type cachePayload struct {
	Enabled        bool     `json:"enabled"`
	Paths          []string `json:"paths"`
	TTLSeconds     int      `json:"ttlSeconds"`
	MaxTTLSeconds  int      `json:"maxTtlSeconds"`
	MaxObjectBytes int64    `json:"maxObjectBytes"`
	MaxBytes       int64    `json:"maxBytes"`
}

// guardianPayload is the per-host request inspection setting.
type guardianPayload struct {
	Mode         domain.GuardianMode   `json:"mode"`
	Rules        []domain.GuardianRule `json:"rules"`
	MaxURILength int                   `json:"maxUriLength"`
}

// accessLogPayload is the per-host switch for the searchable request log.
type accessLogPayload struct {
	Enabled      bool `json:"enabled"`
	IncludeQuery bool `json:"includeQuery"`
}

// passiveHealthPayload takes its window in seconds for the same reason the
// health check does: the form shows seconds, and milliseconds would invite a
// caller to send 30 and mean thirty seconds.
type passiveHealthPayload struct {
	Enabled         bool `json:"enabled"`
	MaxFails        int  `json:"maxFails"`
	EjectForSeconds int  `json:"ejectForSeconds"`
}

type upstreamPayload struct {
	Scheme        string `json:"scheme"`
	Address       string `json:"address"`
	Weight        int    `json:"weight"`
	MaxConns      int    `json:"maxConns"`
	Enabled       bool   `json:"enabled"`
	SkipTLSVerify bool   `json:"skipTlsVerify"`
}

// healthCheckPayload takes durations as seconds, which is what the form shows;
// milliseconds would invite a caller to send 10 and mean ten seconds.
type healthCheckPayload struct {
	Enabled            bool   `json:"enabled"`
	Path               string `json:"path"`
	IntervalSeconds    int    `json:"intervalSeconds"`
	TimeoutSeconds     int    `json:"timeoutSeconds"`
	HealthyThreshold   int    `json:"healthyThreshold"`
	UnhealthyThreshold int    `json:"unhealthyThreshold"`
	ExpectStatus       int    `json:"expectStatus"`
}

func (p hostPayload) toDomain() domain.Host {
	h := domain.Host{
		Name:             p.Name,
		Enabled:          p.Enabled,
		Domains:          p.Domains,
		Algorithm:        p.Algorithm,
		CertificateID:    p.CertificateID,
		AccessListID:     p.AccessListID,
		ForceHTTPS:       p.ForceHTTPS,
		HSTSMaxAge:       p.HSTSMaxAge,
		WebSocketSupport: p.WebSocketSupport,
		PreserveHost:     p.PreserveHost,
		HealthCheck: domain.HealthCheck{
			Enabled:            p.HealthCheck.Enabled,
			Path:               p.HealthCheck.Path,
			Interval:           secondsToDuration(p.HealthCheck.IntervalSeconds),
			Timeout:            secondsToDuration(p.HealthCheck.TimeoutSeconds),
			HealthyThreshold:   p.HealthCheck.HealthyThreshold,
			UnhealthyThreshold: p.HealthCheck.UnhealthyThreshold,
			ExpectStatus:       p.HealthCheck.ExpectStatus,
		},
		PassiveHealth: domain.PassiveHealth{
			Enabled:  p.PassiveHealth.Enabled,
			MaxFails: p.PassiveHealth.MaxFails,
			EjectFor: secondsToDuration(p.PassiveHealth.EjectForSeconds),
		},
		AccessLog: domain.AccessLogSettings{
			Enabled:      p.AccessLog.Enabled,
			IncludeQuery: p.AccessLog.IncludeQuery,
		},
		Guardian: domain.Guardian{
			Mode:         p.Guardian.Mode,
			Rules:        p.Guardian.Rules,
			MaxURILength: p.Guardian.MaxURILength,
		},
		Cache: domain.Cache{
			Enabled:        p.Cache.Enabled,
			Paths:          p.Cache.Paths,
			TTL:            secondsToDuration(p.Cache.TTLSeconds),
			MaxTTL:         secondsToDuration(p.Cache.MaxTTLSeconds),
			MaxObjectBytes: p.Cache.MaxObjectBytes,
			MaxBytes:       p.Cache.MaxBytes,
		},
		TrafficLimits: domain.TrafficLimits{
			Mode:              p.TrafficLimits.Mode,
			RequestsPerSecond: p.TrafficLimits.RequestsPerSecond,
			Burst:             p.TrafficLimits.Burst,
			MaxConcurrent:     p.TrafficLimits.MaxConcurrent,
			MaxBodyBytes:      p.TrafficLimits.MaxBodyBytes,
			Exempt:            p.TrafficLimits.Exempt,
		},
	}
	for _, u := range p.Upstreams {
		h.Upstreams = append(h.Upstreams, domain.Upstream{
			Scheme:        u.Scheme,
			Address:       u.Address,
			Weight:        u.Weight,
			MaxConns:      u.MaxConns,
			Enabled:       u.Enabled,
			SkipTLSVerify: u.SkipTLSVerify,
		})
	}
	return h
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.opts.Hosts.List(r.Context())
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, hosts)
}

func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	host, err := s.opts.Hosts.Get(r.Context(), id)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, host)
}

func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	var payload hostPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, s.logger, err)
		return
	}

	host := payload.toDomain()
	if err := s.prepareHost(r, &host); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.Hosts.Create(r.Context(), &host); err != nil {
		writeError(w, s.logger, err)
		return
	}

	s.logger.Info("host created", "name", host.Name, "domains", host.Domains)
	s.applyConfig(r)
	writeJSON(w, s.logger, http.StatusCreated, host)
}

func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	var payload hostPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, s.logger, err)
		return
	}

	host := payload.toDomain()
	host.ID = id
	if err := s.prepareHost(r, &host); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.Hosts.Update(r.Context(), &host); err != nil {
		writeError(w, s.logger, err)
		return
	}

	s.logger.Info("host updated", "name", host.Name, "id", host.ID)
	s.applyConfig(r)
	writeJSON(w, s.logger, http.StatusOK, host)
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.Hosts.Delete(r.Context(), id); err != nil {
		writeError(w, s.logger, err)
		return
	}

	s.logger.Info("host deleted", "id", id)
	s.applyConfig(r)
	writeJSON(w, s.logger, http.StatusNoContent, nil)
}

// prepareHost normalises and validates a host, and checks the cross-record
// rules the domain type cannot see on its own.
func (s *Server) prepareHost(r *http.Request, h *domain.Host) error {
	h.Normalize()
	if err := h.Validate(); err != nil {
		return err
	}

	if h.CertificateID != nil {
		if _, err := s.opts.Certs.Get(r.Context(), *h.CertificateID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				v := &domain.ValidationError{}
				v.Add("certificateId", "no certificate has id %d", *h.CertificateID)
				return v
			}
			return err
		}
	}

	if h.AccessListID != nil {
		if _, err := s.opts.AccessLists.Get(r.Context(), *h.AccessListID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				v := &domain.ValidationError{}
				v.Add("accessListId", "no access list has id %d", *h.AccessListID)
				return v
			}
			return err
		}
	}
	return nil
}

// handleListAlgorithms lets the UI render the algorithm picker from the server
// rather than keeping its own copy of the list.
func (s *Server) handleListAlgorithms(w http.ResponseWriter, _ *http.Request) {
	type entry struct {
		Value       domain.Algorithm `json:"value"`
		Label       string           `json:"label"`
		Description string           `json:"description"`
	}
	writeJSON(w, s.logger, http.StatusOK, []entry{
		{domain.RoundRobin, "Round robin",
			"Sends each request to the next upstream in turn."},
		{domain.WeightedRoundRobin, "Weighted round robin",
			"Round robin in proportion to each upstream's weight, interleaved rather than in bursts."},
		{domain.LeastConnections, "Least connections",
			"Sends each request to whichever upstream has the fewest in flight."},
		{domain.IPHash, "IP hash",
			"Pins each client address to one upstream, giving sticky sessions without cookies."},
	})
}

// handleListGuardianRules describes the rules from the server, so the form
// explains what each one costs in false positives rather than leaving an
// operator to guess from its name.
func (s *Server) handleListGuardianRules(w http.ResponseWriter, _ *http.Request) {
	type entry struct {
		Value         domain.GuardianRule `json:"value"`
		Label         string              `json:"label"`
		Description   string              `json:"description"`
		SafeByDefault bool                `json:"safeByDefault"`
	}
	described := map[domain.GuardianRule]struct{ label, detail string }{
		domain.RulePathTraversal: {"Path traversal",
			"Attempts to climb out of the web root, including encoded forms. Almost never a real request."},
		domain.RuleSensitiveFiles: {"Sensitive files",
			"Probes for .env, .git, SSH keys, database dumps and the like. Nothing should serve these."},
		domain.RuleControlCharacters: {"Control characters",
			"Null bytes and other control bytes in the path. No legitimate client sends them."},
		domain.RuleScannerAgents: {"Scanner user agents",
			"Tools that announce themselves, such as sqlmap and nikto. Stops the lazy, which is most of the noise."},
		domain.RuleSQLInjection: {"SQL injection",
			"SQL fragments in the path or query. Can match a search box, so watch it in detect mode first."},
		domain.RuleShellInjection: {"Shell injection",
			"Command chaining such as ;cat /etc/passwd. Can match legitimate input, so watch it first."},
	}

	out := make([]entry, 0, len(domain.GuardianRules()))
	for _, r := range domain.GuardianRules() {
		d := described[r]
		out = append(out, entry{Value: r, Label: d.label, Description: d.detail,
			SafeByDefault: r.SafeByDefault()})
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func pathID(r *http.Request) (int64, error) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.Join(errBadRequest, errors.New("the id in the path is not valid"))
	}
	return id, nil
}

// secondsToDuration converts a form value. A non-positive value is left at
// zero so Host.Normalize can apply its own default rather than this layer
// having a second copy of it.
func secondsToDuration(s int) time.Duration {
	if s <= 0 {
		return 0
	}
	return time.Duration(s) * time.Second
}
