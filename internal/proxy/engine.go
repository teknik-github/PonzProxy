package proxy

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/alerts"
	"github.com/ponzproxy/ponzproxy/internal/cache"
	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/health"
	"github.com/ponzproxy/ponzproxy/internal/limiter"
	"github.com/ponzproxy/ponzproxy/internal/metrics"
)

// CertificateResolver supplies the TLS certificate for a server name. It is
// implemented by certmgr; declaring it here keeps proxy free of that
// dependency and makes the engine trivial to test with a stub.
type CertificateResolver interface {
	// CertificateFor returns the certificate to present for a handshake,
	// or an error when none is available. It takes the whole ClientHello
	// because a TLS-ALPN-01 validation is identified by its ALPN list, not
	// by the server name alone.
	CertificateFor(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
	// HandleACMEChallenge answers an HTTP-01 challenge. It reports false
	// when the request is not a challenge, so the engine routes it onward.
	HandleACMEChallenge(w http.ResponseWriter, r *http.Request) bool
}

// AccessRecorder receives one entry per proxied request for hosts that have
// logging switched on. It is an interface so the engine does not depend on the
// writer, and so a test can assert what was recorded.
//
// Implementations must not block: this is called on the request path.
type AccessRecorder interface {
	Record(domain.AccessLogEntry)
}

// Options configures the engine. Only Logger and Collector are required.
type Options struct {
	Logger    *slog.Logger
	Collector *metrics.Collector
	Health    *health.Checker
	Certs     CertificateResolver
	// AccessLog records requests for hosts that ask for it. Optional.
	AccessLog AccessRecorder
	// Alerts is told when passive health ejects a backend. Optional, and
	// never waited on: Raise must not block.
	Alerts alerts.Raiser

	// TrustedClientIPHeader, when set, takes the client address from that
	// header instead of the connection. See config.Config for the warning
	// that comes with it.
	TrustedClientIPHeader string

	// MaxRetries bounds how many additional backends one request may be
	// tried against after a connection failure.
	MaxRetries int
}

// Config is everything the data plane serves. It is passed as a struct rather
// than a growing parameter list because each new routing feature would
// otherwise change Reload's signature and every caller with it.
type Config struct {
	Hosts     []domain.Host
	Redirects []domain.Redirect
	// AccessLists is keyed by id so a route can resolve its own at build
	// time, keeping the request path free of lookups.
	AccessLists map[int64]*domain.AccessList
}

// Engine is the data plane. One instance serves every host on the HTTP and
// HTTPS listeners.
type Engine struct {
	opts   Options
	logger *slog.Logger

	// table is swapped wholesale on reload. Readers load it once per
	// request, so configuration changes never block traffic and never tear
	// a request between two configurations.
	table atomic.Pointer[routingTable]

	// transports are shared across all routes. Go's transport pools
	// connections per destination internally, so two instances — verifying
	// and non-verifying — are enough for every backend.
	verifying   *http.Transport
	insecure    *http.Transport
	reloadMu    sync.Mutex
	certResolve CertificateResolver

	// reverseProxy is shared by every route; per-request routing travels
	// through the request context rather than through separate instances.
	reverseProxy *httputil.ReverseProxy

	// httpsPort is used to build force-HTTPS redirects when the TLS
	// listener is not on the default port.
	httpsPort string

	// authCache keeps basic auth off the bcrypt path for repeat requests.
	authCache *authCache

	// caches holds the per-host static asset cache.
	caches *cache.Store

	// limiter holds per-client budgets for hosts with traffic limits on.
	// Hosts with limits off never touch it.
	limiter *limiter.Limiter
}

// NewEngine builds the data plane. Call Reload before serving to install a
// configuration.
func NewEngine(opts Options) *Engine {
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = 2
	}
	e := &Engine{
		opts:        opts,
		logger:      opts.Logger.With("component", "proxy"),
		verifying:   newBackendTransport(false),
		insecure:    newBackendTransport(true),
		certResolve: opts.Certs,
	}
	e.table.Store(buildRoutingTable(Config{}, nil))
	e.reverseProxy = e.buildReverseProxy()
	e.authCache = newAuthCache()
	e.caches = cache.NewStore()
	e.limiter = limiter.New()
	return e
}

// SetHTTPSPort tells the engine which port force-HTTPS redirects should point
// at. It is only needed when the TLS listener is not on 443.
func (e *Engine) SetHTTPSPort(port string) { e.httpsPort = port }

// Reload installs a new configuration. It is safe to call while serving.
func (e *Engine) Reload(cfg Config) {
	// Serialising reloads keeps the previous table stable while the next
	// one inherits pools from it; requests are unaffected either way.
	e.reloadMu.Lock()
	defer e.reloadMu.Unlock()

	table := buildRoutingTable(cfg, e.table.Load())
	e.table.Store(table)

	if e.opts.Health != nil {
		targets := make([]health.Target, 0, len(table.routes))
		for _, r := range table.routes {
			if !r.host.Enabled {
				continue
			}
			targets = append(targets, health.Target{
				HostID: r.host.ID,
				Name:   r.host.Name,
				Pool:   r.pool,
				Check:  r.host.HealthCheck,
			})
			// A location has its own backends, so it needs its own probe
			// loop. It borrows the host's check settings: how a backend is
			// tested is a property of the site, not of the path.
			for _, l := range r.locations {
				targets = append(targets, health.Target{
					HostID:   r.host.ID,
					Name:     r.host.Name + " " + l.config.Path,
					Pool:     l.pool,
					Check:    r.host.HealthCheck,
					Location: l.config.Path,
				})
			}
		}
		e.opts.Health.Sync(targets)
	}

	if e.opts.Collector != nil {
		keep := make(map[int64]struct{}, len(table.routes))
		for _, r := range table.routes {
			keep[r.host.ID] = struct{}{}
		}
		e.opts.Collector.Forget(keep)
	}

	// A host that was deleted, or whose operator just switched caching off,
	// must release its objects rather than holding them until restart.
	cached := make(map[int64]struct{}, len(table.routes))
	for _, r := range table.routes {
		if r.host.Cache.Active() {
			cached[r.host.ID] = struct{}{}
		}
	}
	e.forgetCaches(cached)

	// A host whose operator just switched limits off must not keep its
	// clients' spent budgets, or turning limits back on would apply a
	// window from before the change.
	for _, r := range table.routes {
		if !r.host.TrafficLimits.Enabled() {
			e.limiter.Forget(r.host.ID)
		}
	}

	e.logger.Info("configuration reloaded",
		"hosts", len(table.routes),
		"redirects", len(cfg.Redirects),
		"accessLists", len(cfg.AccessLists))
}

// MetricsPools implements metrics.PoolProvider, exposing live backend state
// for the dashboard.
func (e *Engine) MetricsPools() []metrics.PoolInfo {
	table := e.table.Load()
	out := make([]metrics.PoolInfo, 0, len(table.routes))
	for _, r := range table.routes {
		up, total := r.pool.HealthyCount()
		ups := r.pool.Snapshot()

		// A location's backends belong to the same host, so they are
		// reported in the same flat list with a label rather than as a
		// second entry — PoolInfo is keyed by host id downstream, and two
		// entries per host would silently drop one of them.
		for _, l := range r.locations {
			lup, ltotal := l.pool.HealthyCount()
			up, total = up+lup, total+ltotal
			for _, s := range l.pool.Snapshot() {
				s.Location = l.config.Path
				ups = append(ups, s)
			}
		}

		out = append(out, metrics.PoolInfo{
			HostID:    r.host.ID,
			Name:      r.host.Name,
			Enabled:   r.host.Enabled,
			Upstreams: ups,
			Up:        up,
			Total:     total,
		})
	}
	return out
}

// TLSConfig returns the configuration for the HTTPS listener. Certificates are
// resolved per handshake from the routing table, so adding a host with a
// certificate takes effect without restarting the listener.
func (e *Engine) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// acme-tls/1 must be offered for TLS-ALPN-01 validation to
		// succeed; the CA negotiates it explicitly and nothing else does.
		NextProtos: []string{"h2", "http/1.1", "acme-tls/1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if e.certResolve == nil {
				return nil, errNoCertificate
			}
			return e.certResolve.CertificateFor(hello)
		},
	}
}

// newBackendTransport builds the transport used to reach upstreams.
//
// The timeouts are deliberately generous on the response side — a backend may
// legitimately stream for a long time — but tight on connection setup, which
// is where a dead backend actually shows up and where a retry is still safe.
func newBackendTransport(skipVerify bool) *http.Transport {
	return &http.Transport{
		Proxy: nil, // upstreams are reached directly, never via HTTP_PROXY
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// ForceAttemptHTTP2 lets an https upstream negotiate h2 while an
		// http one stays on HTTP/1.1, which is what backends expect.
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: skipVerify,
		},
	}
}

// transportFor picks the shared transport matching an upstream's TLS policy.
func (e *Engine) transportFor(skipVerify bool) *http.Transport {
	if skipVerify {
		return e.insecure
	}
	return e.verifying
}

// Close releases pooled upstream connections.
func (e *Engine) Close() {
	e.verifying.CloseIdleConnections()
	e.insecure.CloseIdleConnections()
	e.limiter.Close()
}
