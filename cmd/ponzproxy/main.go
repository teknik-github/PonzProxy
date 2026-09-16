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
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/accesslog"
	"github.com/ponzproxy/ponzproxy/internal/alerts"
	"github.com/ponzproxy/ponzproxy/internal/api"
	"github.com/ponzproxy/ponzproxy/internal/backup"
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
	// The only flag the process takes. Everything else is configured by
	// environment variables, because everything else is a setting a running
	// container needs, while this is a one-shot recovery action.
	resetUser := flag.String("reset-password", "",
		"reset the named account to a freshly generated password, print it, and exit")
	backupTo := flag.String("backup", "",
		"write a backup of the data directory to this file, then exit")
	restoreFrom := flag.String("restore", "",
		"replace the data directory from this backup file, then exit")
	flag.Parse()

	// A restore is about to replace the data directory, so the config is
	// read without creating it — see config.LoadReadOnly.
	if *restoreFrom != "" {
		cfg, err := config.LoadReadOnly()
		if err != nil {
			return err
		}
		return restoreBackup(cfg, *restoreFrom)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if *resetUser != "" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return resetPassword(ctx, cfg, *resetUser)
	}
	if *backupTo != "" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return writeBackup(ctx, cfg, *backupTo)
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

	backups := backup.NewScheduler(db.DB(), cfg.DataDir, version,
		cfg.BackupEvery, cfg.BackupKeep, logger)

	collector := metrics.New(db.Metrics(), logger)
	dispatcher := alerts.New(db.AlertChannels(), logger)
	accessLog := accesslog.New(db.AccessLog(), accesslog.Options{
		Retention: cfg.AccessLogRetention,
		MaxRows:   cfg.AccessLogMaxRows,
	}, logger)
	checker := health.New(logger, nil)
	checker.SetAlerts(dispatcher)
	defer checker.Close()

	certs, err := certmgr.New(ctx, db.Certificates(), certmgr.Config{
		Dir:          cfg.CertDir(),
		DirectoryURL: cfg.ACMEDirectory,
		Email:        cfg.ACMEEmail,
	}, logger)
	if err != nil {
		return err
	}
	certs.SetAlerts(dispatcher)

	engine := proxy.NewEngine(proxy.Options{
		Logger:                logger,
		Collector:             collector,
		Health:                checker,
		Certs:                 certs,
		AccessLog:             accessLog,
		Alerts:                dispatcher,
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
		Hosts:            db.Hosts(),
		Certs:            db.Certificates(),
		Users:            db.Users(),
		Metrics:          db.Metrics(),
		AccessLists:      db.AccessLists(),
		Redirects:        db.Redirects(),
		AccessLogs:       db.AccessLog(),
		AlertChannels:    db.AlertChannels(),
		ReloadAlerts:     dispatcher.Reload,
		AlertTester:      dispatcher.Test,
		AlertStats:       dispatcher.Stats,
		AccessLogStats:   accessLog.Stats,
		CertManager:      certs,
		Collector:        collector,
		JWTSecret:        cfg.JWTSecret,
		SessionTTL:       cfg.SessionTTL,
		DataDir:          cfg.DataDir,
		DB:               db.DB(),
		Version:          version,
		Snapshot:         backups.Snapshot,
		BackupEvery:      cfg.BackupEvery,
		BackupKeep:       cfg.BackupKeep,
		ApplyConfig:      applyConfig,
		UI:               ui,
		MetricsRetention: cfg.MetricsRetention,
		Logger:           logger,
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
	spawn("alerts", dispatcher.Run)
	spawn("backups", backups.Run)
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

// resetPassword generates a new password for an existing account, stores it
// and prints it. It is the only way back in for an operator who has forgotten
// the last administrator's password: the dashboard can change a password but
// not recover one, and the alternative is editing the SQLite file by hand.
//
// Requiring shell access to the machine is the whole security model here —
// anyone who has that can already edit the database directly, so this adds no
// reachable privilege. For the same reason the new password is generated
// rather than taken as an argument: a password on the command line ends up in
// the shell history and in every ps listing on the box.
//
// It is safe to run while the server is up. SQLite is opened in WAL mode, and
// the API reads the stored hash on every sign-in, so the new password works
// straight away without a restart.
func resetPassword(ctx context.Context, cfg *config.Config, username string) error {
	db, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close()

	users := db.Users()
	name := domain.NormalizeUsername(username)
	user, err := users.GetByUsername(ctx, name)
	if errors.Is(err, domain.ErrNotFound) {
		// Someone who has forgotten a password may well have forgotten the
		// username too, and this is a local-only command, so naming the
		// accounts costs nothing and saves a round of guessing.
		return fmt.Errorf("no account named %q%s", name, knownAccounts(ctx, users))
	}
	if err != nil {
		return fmt.Errorf("look up %q: %w", name, err)
	}

	password, err := randomPassword()
	if err != nil {
		return err
	}
	hash, err := api.HashPassword(password)
	if err != nil {
		return err
	}
	if err := users.UpdatePassword(ctx, user.ID, hash); err != nil {
		return fmt.Errorf("reset the password for %q: %w", user.Username, err)
	}

	// The password alone goes to stdout so the command can be piped into a
	// password manager; the prose goes to stderr so piping does not capture
	// it. Sessions already signed in are JWTs and stay valid until they
	// expire — this closes the front door, not the ones already open.
	fmt.Fprintf(os.Stderr, "\n  new password for %q (%s):\n\n    ", user.Username, user.Role)
	fmt.Println(password)
	fmt.Fprintf(os.Stderr, "\n  Shown once. Existing sessions stay valid until they expire.\n\n")
	return nil
}

// writeBackup copies the data directory into one archive and exits. It is the
// scriptable half of the console's Download button, for a cron job that copies
// the result somewhere else — which is the only kind of backup that survives
// losing this machine.
func writeBackup(ctx context.Context, cfg *config.Config, path string) error {
	db, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close()

	// Refusing to overwrite is deliberate: a backup command that silently
	// replaces a file is one keystroke away from destroying the copy it was
	// meant to add to.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := backup.Create(ctx, db.DB(), cfg.DataDir, version, f); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n  wrote %s (%d bytes)\n"+
		"  This archive contains every private key and the session secret.\n"+
		"  Treat it exactly as you would the server itself.\n\n", path, info.Size())
	return nil
}

// restoreBackup replaces the data directory from an archive.
//
// It is a separate invocation rather than an API call because a running proxy
// holds the database open: swapping the file underneath it would leave the
// process serving from a handle to a file that no longer exists. Stop the
// service, restore, start it again.
func restoreBackup(cfg *config.Config, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	format, madeBy, created, err := backup.Describe(f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "\n  %s\n    format:  %s\n    written: %s by %s\n\n",
		path, format, created.Format(time.RFC1123), madeBy)

	moved, err := backup.Restore(cfg.DataDir, f)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "  restored into %s\n", cfg.DataDir)
	if moved != "" {
		// Nothing was deleted. A restore is run in exactly the situation
		// where a second mistake is most likely, so the old directory is
		// left for the operator to remove once they are satisfied.
		fmt.Fprintf(os.Stderr, "  the previous data directory is at %s\n"+
			"  check the proxy starts, then remove it yourself\n", moved)
	}
	fmt.Fprint(os.Stderr, "\n")
	return nil
}

// knownAccounts renders ", known accounts: a, b" for an error message, or an
// empty string when the list cannot be read — a failure to list is not worth
// replacing the real error with.
func knownAccounts(ctx context.Context, users domain.UserRepository) string {
	all, err := users.List(ctx)
	if err != nil || len(all) == 0 {
		return ""
	}
	names := make([]string, 0, len(all))
	for _, u := range all {
		names = append(names, u.Username)
	}
	return ". Known accounts: " + strings.Join(names, ", ")
}

func randomPassword() (string, error) {
	raw := make([]byte, 18) // 144 bits, 24 characters once encoded
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate a password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
