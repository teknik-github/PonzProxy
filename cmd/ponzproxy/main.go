// Command ponzproxy is a load balancer and reverse proxy with a live web UI.
//
// It listens on three ports: the proxy's HTTP and HTTPS listeners carry user
// traffic, and a separate admin listener serves the REST API, the WebSocket
// feed and the dashboard. Keeping them apart means the control plane can never
// interfere with proxied traffic.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/accesslog"
	"github.com/ponzproxy/ponzproxy/internal/api"
	"github.com/ponzproxy/ponzproxy/internal/certmgr"
	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/health"
	"github.com/ponzproxy/ponzproxy/internal/metrics"
	"github.com/ponzproxy/ponzproxy/internal/platform/config"
	"github.com/ponzproxy/ponzproxy/internal/platform/logging"
	"github.com/ponzproxy/ponzproxy/internal/proxy"
	"github.com/ponzproxy/ponzproxy/internal/store"
	"github.com/ponzproxy/ponzproxy/internal/webui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ponzproxy: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logging.New(cfg.LogLevel, cfg.LogFormat)
	logger.Info("starting ponzproxy", "version", version, "dataDir", cfg.DataDir)

	// Cancelled on SIGINT or SIGTERM; every long-running component takes
	// this context and unwinds from it.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close()

	if err := bootstrapAdmin(ctx, db, logger); err != nil {
		return err
	}

	collector := metrics.New(db.Metrics(), logger)
	accessLog := accesslog.New(db.AccessLog(), accesslog.Options{
		Retention: cfg.AccessLogRetention,
		MaxRows:   cfg.AccessLogMaxRows,
	}, logger)
	checker := health.New(logger, nil)
	defer checker.Close()

	certs, err := certmgr.New(ctx, db.Certificates(), certmgr.Config{
		Dir:          cfg.CertDir(),
		DirectoryURL: cfg.ACMEDirectory,
		Email:        cfg.ACMEEmail,
	}, logger)
	if err != nil {
		return err
	}

	engine := proxy.NewEngine(proxy.Options{
		Logger:                logger,
		Collector:             collector,
		Health:                checker,
		Certs:                 certs,
		AccessLog:             accessLog,
		TrustedClientIPHeader: cfg.TrustedProxyHeader,
	})
	defer engine.Close()

	// The collector reports per-backend health, which only the engine knows,
	// and the engine reports traffic, which only the collector knows. The
	// cycle is broken by wiring one direction after construction.
	collector.SetPoolProvider(engine)

	if _, port, err := net.SplitHostPort(cfg.HTTPSAddr); err == nil {
		engine.SetHTTPSPort(port)
	}

	// applyConfig is the single path by which stored configuration reaches
	// the running proxy. The API calls it after every change.
	applyConfig := func(ctx context.Context) error {
		hosts, err := db.Hosts().List(ctx)
		if err != nil {
			return fmt.Errorf("load hosts: %w", err)
		}
		redirects, err := db.Redirects().List(ctx)
		if err != nil {
			return fmt.Errorf("load redirects: %w", err)
		}
		lists, err := db.AccessLists().List(ctx)
		if err != nil {
			return fmt.Errorf("load access lists: %w", err)
		}
		// Keyed by id so each route resolves its own list once, at build
		// time, rather than searching on every request.
		byID := make(map[int64]*domain.AccessList, len(lists))
		for i := range lists {
			byID[lists[i].ID] = &lists[i]
		}

		engine.Reload(proxy.Config{
			Hosts:       hosts,
			Redirects:   redirects,
			AccessLists: byID,
		})
		return nil
	}
	if err := applyConfig(ctx); err != nil {
		return err
	}

	ui, err := webui.Handler()
	if err != nil {
		if !errors.Is(err, webui.ErrNotBuilt) {
			return err
		}
		// The API is still fully usable without the dashboard, so this is
		// a warning rather than a failure to start.
		logger.Warn("the web UI is not embedded in this binary; serving the API only",
			"hint", "run `make ui` before `make build`")
	}

	apiServer := api.NewServer(ctx, api.Options{
		Hosts:          db.Hosts(),
		Certs:          db.Certificates(),
		Users:          db.Users(),
		Metrics:        db.Metrics(),
		AccessLists:    db.AccessLists(),
		Redirects:      db.Redirects(),
		AccessLogs:     db.AccessLog(),
		AccessLogStats: accessLog.Stats,
		CertManager:    certs,
		Collector:      collector,
		JWTSecret:      cfg.JWTSecret,
		SessionTTL:     cfg.SessionTTL,
		ApplyConfig:    applyConfig,
		UI:             ui,
		Logger:         logger,
	})

	var background sync.WaitGroup
	spawn := func(name string, fn func(context.Context)) {
		background.Add(1)
		go func() {
			defer background.Done()
			fn(ctx)
			logger.Debug("background task stopped", "task", name)
		}()
	}
	spawn("metrics", func(ctx context.Context) {
		collector.Run(ctx, cfg.MetricsFlush, cfg.MetricsRetention)
	})
	spawn("certificates", certs.Run)
	spawn("accesslog", accessLog.Run)
	spawn("websocket", apiServer.Run)

	servers := []*namedServer{
		{name: "proxy-http", addr: cfg.HTTPAddr, server: &http.Server{
			Addr:    cfg.HTTPAddr,
			Handler: engine,
			// Generous, because a proxied request may legitimately stream
			// for a long time. ReadHeaderTimeout is the one that matters
			// for turning away a slow-loris client.
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelDebug),
		}},
		{name: "proxy-https", addr: cfg.HTTPSAddr, tls: true, server: &http.Server{
			Addr:              cfg.HTTPSAddr,
			Handler:           engine,
			TLSConfig:         engine.TLSConfig(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelDebug),
		}},
		{name: "admin", addr: cfg.AdminAddr, server: &http.Server{
			Addr:              cfg.AdminAddr,
			Handler:           apiServer,
			ReadHeaderTimeout: 10 * time.Second,
			// The admin API has no streaming endpoints except the
			// WebSocket feed, which net/http exempts from these once the
			// connection is hijacked.
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
			ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelDebug),
		}},
	}

	// A listener that fails to bind must stop the process: silently serving
	// on two of three ports would look like success.
	failed := make(chan error, len(servers))
	for _, s := range servers {
		if err := s.listen(); err != nil {
			return err
		}
		logger.Info("listening", "listener", s.name, "addr", s.addr, "tls", s.tls)
	}
	for _, s := range servers {
		go func(s *namedServer) {
			if err := s.serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				failed <- fmt.Errorf("%s listener: %w", s.name, err)
			}
		}(s)
	}

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-failed:
		logger.Error("a listener stopped unexpectedly", "error", err)
		stop()
		return err
	}

	// Shutdown gives in-flight requests a chance to finish before the
	// process exits. The deadline is its own context because ctx is
	// already cancelled by this point.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func(s *namedServer) {
			defer wg.Done()
			if err := s.server.Shutdown(shutdownCtx); err != nil {
				logger.Warn("listener did not shut down cleanly", "listener", s.name, "error", err)
			}
		}(s)
	}
	wg.Wait()
	background.Wait()

	logger.Info("stopped")
	return nil
}

// namedServer pairs a server with the listener it binds, so binding can be
// checked before any goroutine is started.
type namedServer struct {
	name     string
	addr     string
	tls      bool
	server   *http.Server
	listener net.Listener
}

func (s *namedServer) listen() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("bind %s listener on %s: %w", s.name, s.addr, err)
	}
	s.listener = ln
	return nil
}

func (s *namedServer) serve() error {
	if s.tls {
		// The certificates come from TLSConfig.GetCertificate, so no files
		// are named here.
		return s.server.ServeTLS(s.listener, "", "")
	}
	return s.server.Serve(s.listener)
}

// bootstrapAdmin creates the first account on a fresh database and prints its
// generated password once. A generated password is used rather than a fixed
// default so an installation that is never configured is not trivially
// reachable by anyone who knows the project.
func bootstrapAdmin(ctx context.Context, db *store.Store, logger *slog.Logger) error {
	password, err := randomPassword()
	if err != nil {
		return err
	}

	created, err := api.EnsureBootstrapUser(ctx, db.Users(), password, api.DefaultPasswordCost)
	if err != nil {
		return fmt.Errorf("create the initial admin account: %w", err)
	}
	if !created {
		return nil
	}

	// Printed to stderr rather than logged, so it is not swept into a log
	// aggregator along with everything else.
	fmt.Fprintf(os.Stderr, "\n"+
		"  ponzproxy created its first account.\n"+
		"    username: admin\n"+
		"    password: %s\n"+
		"  This is shown once. Change it after signing in.\n\n", password)
	logger.Info("created the initial admin account", "username", "admin")
	return nil
}

func randomPassword() (string, error) {
	raw := make([]byte, 18) // 144 bits, 24 characters once encoded
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate a password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
