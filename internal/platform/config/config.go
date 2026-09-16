// Package config carries the process-level settings for ponzproxy. Everything
// that can change at runtime (hosts, upstreams, certificates) lives in the
// store instead; this file only covers what must be known before boot.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// DataDir holds the SQLite database and the certificate cache.
	DataDir string

	// HTTPAddr and HTTPSAddr are the public proxy listeners.
	HTTPAddr  string
	HTTPSAddr string

	// AdminAddr serves the REST API, the WebSocket feed and the embedded UI.
	AdminAddr string

	// JWTSecret signs admin sessions. Generated and persisted on first boot
	// when the environment does not supply one.
	JWTSecret []byte

	// SessionTTL bounds how long an admin session stays valid.
	SessionTTL time.Duration

	// ACMEDirectory points at the CA. Defaults to Let's Encrypt production;
	// point it at the staging endpoint while testing to avoid rate limits.
	ACMEDirectory string
	ACMEEmail     string

	// MetricsFlush is how often in-memory counters are folded into a
	// persisted sample, and MetricsRetention how long samples are kept.
	MetricsFlush     time.Duration
	MetricsRetention time.Duration

	// AccessLogRetention and AccessLogMaxRows bound the searchable request
	// log. The row cap is the one that makes the worst case predictable:
	// age alone cannot stop a traffic burst from filling a disk.
	AccessLogRetention time.Duration
	AccessLogMaxRows   int

	// BackupEvery is how often a local snapshot of the data directory is
	// written, and BackupKeep how many are kept. A local copy protects
	// against operator error and corruption; it is on the same disk, so it
	// does not protect against losing that disk. Zero disables it.
	BackupEvery time.Duration
	BackupKeep  int

	// LogLevel and LogFormat configure the process logger.
	LogLevel  string
	LogFormat string

	// TrustedProxyHeader, when set (e.g. "X-Forwarded-For"), lets ponzproxy
	// take the client IP from that header. Only enable it when ponzproxy
	// itself sits behind a trusted proxy: it is spoofable otherwise, which
	// would let a client steer IP-hash routing.
	TrustedProxyHeader string
}

const (
	LetsEncryptProduction = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// Load builds a Config from the environment and prepares the data directory,
// so callers can assume it exists and that a session secret is in place.
func Load() (*Config, error) {
	c, err := parse()
	if err != nil {
		return nil, err
	}
	if err := c.prepare(); err != nil {
		return nil, err
	}
	return c, nil
}

// LoadReadOnly builds the same Config without touching the disk.
//
// It exists for --restore, which is about to replace the data directory: Load
// would create that directory and write a fresh session secret into it, and
// the restore would then dutifully report moving aside a directory it had
// manufactured seconds earlier. JWTSecret is left unset, which is safe because
// no command using this ever serves a request.
func LoadReadOnly() (*Config, error) { return parse() }

func parse() (*Config, error) {
	c := &Config{
		DataDir:            env("PONZ_DATA_DIR", "./data"),
		HTTPAddr:           env("PONZ_HTTP_ADDR", ":80"),
		HTTPSAddr:          env("PONZ_HTTPS_ADDR", ":443"),
		AdminAddr:          env("PONZ_ADMIN_ADDR", ":8080"),
		ACMEDirectory:      env("PONZ_ACME_DIRECTORY", LetsEncryptProduction),
		ACMEEmail:          env("PONZ_ACME_EMAIL", ""),
		SessionTTL:         envDuration("PONZ_SESSION_TTL", 24*time.Hour),
		MetricsFlush:       envDuration("PONZ_METRICS_FLUSH", 10*time.Second),
		MetricsRetention:   envDuration("PONZ_METRICS_RETENTION", 30*24*time.Hour),
		AccessLogRetention: envDuration("PONZ_ACCESS_LOG_RETENTION", 7*24*time.Hour),
		AccessLogMaxRows:   envInt("PONZ_ACCESS_LOG_MAX_ROWS", 500_000),
		BackupEvery:        envDuration("PONZ_BACKUP_EVERY", 24*time.Hour),
		BackupKeep:         envInt("PONZ_BACKUP_KEEP", 7),
		LogLevel:           env("PONZ_LOG_LEVEL", "info"),
		LogFormat:          env("PONZ_LOG_FORMAT", "text"),

		TrustedProxyHeader: env("PONZ_TRUSTED_PROXY_HEADER", ""),
	}

	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}
	c.DataDir = abs
	return c, nil
}

// prepare creates the data directory and the session secret.
func (c *Config) prepare() error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	secret, err := loadOrCreateSecret(c.DataDir)
	if err != nil {
		return err
	}
	c.JWTSecret = secret
	return nil
}

// DBPath is the SQLite file backing config and metrics history.
func (c *Config) DBPath() string { return filepath.Join(c.DataDir, "ponzproxy.db") }

// CertDir stores ACME account keys and issued certificate material.
func (c *Config) CertDir() string { return filepath.Join(c.DataDir, "certs") }

// loadOrCreateSecret keeps admin sessions valid across restarts. PONZ_JWT_SECRET
// wins when set; otherwise a random secret is persisted with 0600 permissions.
func loadOrCreateSecret(dir string) ([]byte, error) {
	if s := os.Getenv("PONZ_JWT_SECRET"); s != "" {
		return []byte(s), nil
	}
	path := filepath.Join(dir, "jwt.secret")
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return b, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate jwt secret: %w", err)
	}
	secret := []byte(hex.EncodeToString(raw))
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, fmt.Errorf("persist jwt secret: %w", err)
	}
	return secret, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envInt reads a whole number, falling back to the default on anything
// unparseable so a typo cannot silently disable a bound.
func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return n
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	// Accept a bare number of seconds too, which is a common mistake.
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}
