package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/accesslog"
	"github.com/ponzproxy/ponzproxy/internal/alerts"
	"github.com/ponzproxy/ponzproxy/internal/api/ws"
	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/metrics"
)

// CertificateService is the slice of certmgr the control plane needs. Keeping
// it an interface here means api does not import certmgr's implementation and
// can be tested with a stub.
type CertificateService interface {
	// Issue runs or renews an ACME order for one certificate.
	Issue(ctx context.Context, id int64) error
	// Reload rebuilds the certificate index after a change.
	Reload(ctx context.Context) error
}

// Options are the collaborators the server needs. Everything is an interface
// or a value, so nothing here reaches back into the data plane directly.
type Options struct {
	Hosts         domain.HostRepository
	Certs         domain.CertificateRepository
	Users         domain.UserRepository
	Metrics       domain.MetricsRepository
	AccessLists   domain.AccessListRepository
	Redirects     domain.RedirectRepository
	AccessLogs    domain.AccessLogRepository
	AlertChannels domain.AlertChannelRepository

	// ReloadAlerts republishes the channel list to the dispatcher, and
	// AlertTester delivers one sample alert. Both optional.
	ReloadAlerts func(context.Context) error
	AlertTester  func(context.Context, *domain.AlertChannel) error
	AlertStats   func() alerts.Stats

	// AccessLogStats reports what the log writer has written and dropped,
	// so the console can show that a history is incomplete. Optional.
	AccessLogStats func() accesslog.Stats

	CertManager CertificateService
	Collector   *metrics.Collector

	JWTSecret  []byte
	SessionTTL time.Duration

	// ApplyConfig republishes the host configuration to the data plane. It
	// is called after every change that affects routing.
	ApplyConfig func(context.Context) error

	// UI serves the embedded dashboard. When nil, only the API is served.
	UI http.Handler

	// LiveInterval is how often the WebSocket feed pushes a snapshot.
	LiveInterval time.Duration

	// DataDir, DB, Version and Snapshot back the backup endpoints. DB is the
	// handle the snapshot is taken from; Snapshot writes one to disk. Both
	// may be nil, in which case those endpoints say so rather than panicking.
	DataDir     string
	DB          *sql.DB
	Version     string
	Snapshot    func(context.Context) (string, error)
	BackupEvery time.Duration
	BackupKeep  int

	// MetricsRetention is how far back samples are kept. The usage report
	// says so, because a bandwidth figure for a window longer than this
	// covers less time than it claims to.
	MetricsRetention time.Duration

	// PasswordCost is the bcrypt cost for this server. Zero selects
	// DefaultPasswordCost; tests lower it so a suite that logs in
	// repeatedly does not spend minutes hashing.
	PasswordCost int

	Logger *slog.Logger
}

// Server is the control plane's HTTP surface.
type Server struct {
	opts    Options
	logger  *slog.Logger
	hub     *ws.Hub
	handler http.Handler

	passwordCost int
	// decoyHash is compared against when a username does not exist, so a
	// failed login costs the same either way.
	decoyHash string
	// logins throttles failed sign-ins per source address.
	logins *loginLimiter

	// baseCtx outlives any single request, so work detached from a request
	// — an ACME order, for one — is not cancelled when the client's
	// connection closes.
	baseCtx context.Context
}

// requiredOptions names the collaborators without which some endpoint would
// panic. They are checked at construction because the alternative is a nil
// dereference in a handler — a 500 discovered by whoever happens to click the
// wrong screen first, long after the wiring mistake was made.
func (o Options) validate() error {
	missing := []string{}
	for name, present := range map[string]bool{
		"Hosts":         o.Hosts != nil,
		"Certs":         o.Certs != nil,
		"Users":         o.Users != nil,
		"Metrics":       o.Metrics != nil,
		"AccessLists":   o.AccessLists != nil,
		"Redirects":     o.Redirects != nil,
		"AccessLogs":    o.AccessLogs != nil,
		"AlertChannels": o.AlertChannels != nil,
		"CertManager":   o.CertManager != nil,
		"Collector":     o.Collector != nil,
		"Logger":        o.Logger != nil,
	} {
		if !present {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("api: these options are required but were not set: %s",
		strings.Join(missing, ", "))
}

// NewServer wires the routes. ctx bounds the lifetime of background work
// started by handlers.
//
// It panics when a required collaborator is missing: that is a wiring mistake
// in the composition root, it cannot be recovered from at runtime, and failing
// at boot is far better than failing on one unlucky request.
func NewServer(ctx context.Context, opts Options) *Server {
	if err := opts.validate(); err != nil {
		panic(err)
	}

	if opts.LiveInterval <= 0 {
		opts.LiveInterval = time.Second
	}
	if opts.PasswordCost <= 0 {
		opts.PasswordCost = DefaultPasswordCost
	}

	s := &Server{
		opts:         opts,
		logger:       opts.Logger.With("component", "api"),
		baseCtx:      ctx,
		passwordCost: opts.PasswordCost,
	}

	decoy, err := newDecoyHash(opts.PasswordCost)
	if err != nil {
		// Without entropy the process cannot hash passwords at all, so
		// there is nothing safe to fall back to.
		panic("ponzproxy: cannot initialise password hashing: " + err.Error())
	}
	s.decoyHash = decoy
	s.logins = newLoginLimiter()
	s.hub = ws.New(s.logger, opts.LiveInterval, func() any {
		return opts.Collector.Snapshot()
	})
	s.handler = s.routes()
	return s
}

// Run drives the background work the control plane needs: the WebSocket
// broadcast loop, and the sweep that reclaims expired rate-limiter entries.
func (s *Server) Run(ctx context.Context) {
	stop := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(stop)
	}()
	go s.logins.run(stop)

	s.hub.Run(ctx)
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// routes builds the mux.
//
// Go's ServeMux matches method and path patterns directly, so the whole
// routing table is visible in one place with no router dependency.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Public: the only endpoints reachable without a session.
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/health", s.handleHealth)

	accessLists := NewAccessListHandlers(AccessListOptions{
		Lists:        s.opts.AccessLists,
		ApplyConfig:  s.opts.ApplyConfig,
		PasswordCost: s.passwordCost,
		Logger:       s.opts.Logger,
	})
	redirects := NewRedirectHandlers(RedirectOptions{
		Redirects:   s.opts.Redirects,
		Certs:       s.opts.Certs,
		ApplyConfig: s.opts.ApplyConfig,
		Logger:      s.opts.Logger,
	})

	// Read-only endpoints, open to both roles.
	read := http.NewServeMux()
	read.HandleFunc("GET /api/auth/me", s.handleCurrentUser)
	read.HandleFunc("POST /api/auth/password", s.handleChangePassword)
	read.HandleFunc("GET /api/hosts", s.handleListHosts)
	read.HandleFunc("GET /api/hosts/{id}", s.handleGetHost)
	read.HandleFunc("GET /api/algorithms", s.handleListAlgorithms)
	read.HandleFunc("GET /api/guardian-rules", s.handleListGuardianRules)
	read.HandleFunc("GET /api/certificates", s.handleListCertificates)
	read.HandleFunc("GET /api/dns-providers", s.handleListDNSProviders)
	read.HandleFunc("GET /api/metrics/live", s.handleLiveSnapshot)
	read.HandleFunc("GET /api/metrics/history", s.handleMetricsHistory)
	read.HandleFunc("GET /api/metrics/usage", s.handleMetricsUsage)
	read.HandleFunc("GET /api/metrics/usage.xlsx", s.handleUsageXLSX)
	read.HandleFunc("GET /api/metrics/usage.pdf", s.handleUsagePDF)
	read.Handle("GET /api/ws", s.hub)
	read.HandleFunc("GET /api/access-lists", accessLists.HandleList)
	read.HandleFunc("GET /api/access-lists/{id}", accessLists.HandleGet)
	read.HandleFunc("GET /api/access-log", s.handleAccessLog)
	read.HandleFunc("GET /api/users", s.handleListUsers)
	read.HandleFunc("GET /api/alert-channels", s.handleListAlertChannels)
	read.HandleFunc("GET /api/alert-events", s.handleListAlertEvents)
	read.HandleFunc("GET /api/alert-stats", s.handleAlertStats)
	mux.Handle("/api/", s.authenticate(read))

	// Mutating endpoints, admins only. They are registered on their own mux
	// so the role check cannot be forgotten on a new route: anything added
	// here is behind it by construction.
	write := http.NewServeMux()
	write.HandleFunc("POST /api/hosts", s.handleCreateHost)
	write.HandleFunc("PUT /api/hosts/{id}", s.handleUpdateHost)
	write.HandleFunc("DELETE /api/hosts/{id}", s.handleDeleteHost)
	write.HandleFunc("POST /api/certificates", s.handleCreateCertificate)
	write.HandleFunc("POST /api/certificates/{id}/issue", s.handleIssueCertificate)
	write.HandleFunc("DELETE /api/certificates/{id}", s.handleDeleteCertificate)
	write.HandleFunc("POST /api/access-lists", accessLists.HandleCreate)
	write.HandleFunc("PUT /api/access-lists/{id}", accessLists.HandleUpdate)
	write.HandleFunc("DELETE /api/access-lists/{id}", accessLists.HandleDelete)
	redirects.Register(read, write)
	write.HandleFunc("POST /api/users", s.handleCreateUser)
	write.HandleFunc("PUT /api/users/{id}/role", s.handleUpdateUserRole)
	write.HandleFunc("POST /api/users/{id}/password", s.handleResetUserPassword)
	write.HandleFunc("DELETE /api/users/{id}", s.handleDeleteUser)
	write.HandleFunc("POST /api/alert-channels", s.handleCreateAlertChannel)
	write.HandleFunc("PUT /api/alert-channels/{id}", s.handleUpdateAlertChannel)
	write.HandleFunc("DELETE /api/alert-channels/{id}", s.handleDeleteAlertChannel)
	write.HandleFunc("POST /api/alert-channels/{id}/test", s.handleTestAlertChannel)
	// Backups are admin-only even to read: the archive carries every private
	// key this proxy holds, so whoever can download one can impersonate
	// every site it serves.
	write.HandleFunc("GET /api/backups", s.handleBackupStatus)
	write.HandleFunc("GET /api/backups/download", s.handleBackupDownload)
	write.HandleFunc("POST /api/backups", s.handleBackupCreate)
	write.HandleFunc("GET /api/backups/{name}", s.handleBackupFile)
	write.HandleFunc("DELETE /api/backups/{name}", s.handleBackupDelete)

	mux.Handle("POST /api/hosts", s.authenticate(s.requireWrite(write)))
	mux.Handle("PUT /api/hosts/{id}", s.authenticate(s.requireWrite(write)))
	mux.Handle("DELETE /api/hosts/{id}", s.authenticate(s.requireWrite(write)))
	mux.Handle("POST /api/certificates", s.authenticate(s.requireWrite(write)))
	mux.Handle("POST /api/certificates/{id}/issue", s.authenticate(s.requireWrite(write)))
	mux.Handle("DELETE /api/certificates/{id}", s.authenticate(s.requireWrite(write)))
	mux.Handle("POST /api/access-lists", s.authenticate(s.requireWrite(write)))
	mux.Handle("PUT /api/access-lists/{id}", s.authenticate(s.requireWrite(write)))
	mux.Handle("DELETE /api/access-lists/{id}", s.authenticate(s.requireWrite(write)))
	mux.Handle("POST /api/users", s.authenticate(s.requireWrite(write)))
	mux.Handle("PUT /api/users/{id}/role", s.authenticate(s.requireWrite(write)))
	mux.Handle("POST /api/users/{id}/password", s.authenticate(s.requireWrite(write)))
	mux.Handle("DELETE /api/users/{id}", s.authenticate(s.requireWrite(write)))
	mux.Handle("POST /api/alert-channels", s.authenticate(s.requireWrite(write)))
	mux.Handle("PUT /api/alert-channels/{id}", s.authenticate(s.requireWrite(write)))
	mux.Handle("DELETE /api/alert-channels/{id}", s.authenticate(s.requireWrite(write)))
	mux.Handle("POST /api/alert-channels/{id}/test", s.authenticate(s.requireWrite(write)))
	mux.Handle("POST /api/redirects", s.authenticate(s.requireWrite(write)))
	mux.Handle("PUT /api/redirects/{id}", s.authenticate(s.requireWrite(write)))
	mux.Handle("DELETE /api/redirects/{id}", s.authenticate(s.requireWrite(write)))
	mux.Handle("GET /api/backups", s.authenticate(s.requireWrite(write)))
	mux.Handle("GET /api/backups/download", s.authenticate(s.requireWrite(write)))
	mux.Handle("POST /api/backups", s.authenticate(s.requireWrite(write)))
	mux.Handle("GET /api/backups/{name}", s.authenticate(s.requireWrite(write)))
	mux.Handle("DELETE /api/backups/{name}", s.authenticate(s.requireWrite(write)))

	if s.opts.UI != nil {
		mux.Handle("/", s.opts.UI)
	}

	return s.recoverPanics(s.securityHeaders(s.logRequests(mux)))
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.logger, http.StatusOK, map[string]any{
		"status":     "ok",
		"dashboards": s.hub.Clients(),
	})
}

// applyConfig republishes routing after a change. A failure here means the
// database and the running proxy have diverged, which is worth an error log
// but not worth failing the operator's already-committed write.
func (s *Server) applyConfig(r *http.Request) {
	if s.opts.ApplyConfig == nil {
		return
	}
	if err := s.opts.ApplyConfig(r.Context()); err != nil {
		s.logger.Error("apply configuration to the proxy", "error", err)
	}
}

// recoverPanics keeps one broken handler from taking the control plane down.
// The data plane runs on separate listeners and is unaffected either way.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				// http.ErrAbortHandler is how a handler deliberately
				// drops a connection; it is not a bug.
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.logger.Error("panic in an API handler",
					"panic", v, "path", r.URL.Path, "stack", string(debug.Stack()))
				writeJSON(w, s.logger, http.StatusInternalServerError,
					errorBody{Error: "the request could not be completed"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders hardens the dashboard. The API is JSON-only and the UI ships
// with the binary, so the policy can be strict: no external origins at all.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Content-Security-Policy",
				"default-src 'self'; style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data:; connect-src 'self' ws: wss:; "+
					"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		}
		next.ServeHTTP(w, r)
	})
}

// logRequests records control-plane activity. The data plane has its own
// access log; this one is about who changed what.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The WebSocket feed is long-lived and would otherwise log one
		// line per dashboard that stays open all day.
		if r.URL.Path == "/api/ws" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Reads are routine; writes and failures are what an operator
		// wants in the log.
		level := slog.LevelDebug
		if r.Method != http.MethodGet || rec.status >= 400 {
			level = slog.LevelInfo
		}
		s.logger.Log(r.Context(), level, "api request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration", time.Since(start).Round(time.Millisecond))
	})
}

// statusRecorder captures the status code for the log line.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
