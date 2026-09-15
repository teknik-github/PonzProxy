package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/store"
)

// open returns a store backed by a fresh database file for one test.
func open(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleHost(name, domainName string) *domain.Host {
	h := &domain.Host{
		Name:        name,
		Enabled:     true,
		Domains:     []string{domainName},
		Algorithm:   domain.RoundRobin,
		HealthCheck: domain.DefaultHealthCheck(),
		Upstreams: []domain.Upstream{
			{Scheme: "http", Address: "10.0.0.1:8080", Weight: 3, Enabled: true},
			{Scheme: "http", Address: "10.0.0.2:8080", Weight: 1, Enabled: true},
		},
	}
	h.Normalize()
	return h
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	for i := range 3 {
		s, err := store.Open(context.Background(), path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		s.Close()
	}
}

func TestHostRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	h := sampleHost("api", "api.example.com")
	if err := s.Hosts().Create(ctx, h); err != nil {
		t.Fatalf("create: %v", err)
	}
	if h.ID == 0 {
		t.Fatal("create did not assign an id")
	}
	for i, u := range h.Upstreams {
		if u.ID == 0 {
			t.Errorf("upstream %d did not get an id", i)
		}
		if u.HostID != h.ID {
			t.Errorf("upstream %d has host id %d, want %d", i, u.HostID, h.ID)
		}
	}

	got, err := s.Hosts().Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "api" || len(got.Domains) != 1 || got.Domains[0] != "api.example.com" {
		t.Errorf("unexpected host read back: %+v", got)
	}
	if len(got.Upstreams) != 2 {
		t.Fatalf("got %d upstreams, want 2", len(got.Upstreams))
	}
	// Order must survive the round trip: weighted round robin depends on it.
	if got.Upstreams[0].Address != "10.0.0.1:8080" || got.Upstreams[0].Weight != 3 {
		t.Errorf("upstream order or weight not preserved: %+v", got.Upstreams)
	}
	if got.HealthCheck.Interval != 10*time.Second {
		t.Errorf("health check interval = %v, want 10s", got.HealthCheck.Interval)
	}
}

func TestHostUpdateReplacesChildren(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	h := sampleHost("api", "api.example.com")
	if err := s.Hosts().Create(ctx, h); err != nil {
		t.Fatalf("create: %v", err)
	}

	h.Domains = []string{"api.example.com", "api2.example.com"}
	h.Upstreams = h.Upstreams[:1]
	h.Algorithm = domain.LeastConnections
	if err := s.Hosts().Update(ctx, h); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.Hosts().Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Domains) != 2 {
		t.Errorf("got %d domains, want 2", len(got.Domains))
	}
	if len(got.Upstreams) != 1 {
		t.Errorf("got %d upstreams, want 1", len(got.Upstreams))
	}
	if got.Algorithm != domain.LeastConnections {
		t.Errorf("algorithm = %q, want least_connections", got.Algorithm)
	}
}

func TestDuplicateDomainIsRejected(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	if err := s.Hosts().Create(ctx, sampleHost("a", "shared.example.com")); err != nil {
		t.Fatalf("create first: %v", err)
	}
	err := s.Hosts().Create(ctx, sampleHost("b", "shared.example.com"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second create error = %v, want ErrConflict", err)
	}

	// The rejected host must not be left half-written.
	hosts, err := s.Hosts().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("got %d hosts after a failed create, want 1", len(hosts))
	}
}

func TestDeleteHostCascades(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	h := sampleHost("api", "api.example.com")
	if err := s.Hosts().Create(ctx, h); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Hosts().Delete(ctx, h.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Hosts().Get(ctx, h.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
	// The domain must be free again, which only holds if the cascade ran.
	if err := s.Hosts().Create(ctx, sampleHost("api2", "api.example.com")); err != nil {
		t.Fatalf("reuse domain after delete: %v", err)
	}
}

func TestCertificateInUseCannotBeDeleted(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	cert := &domain.Certificate{
		Name:    "example",
		Source:  domain.CertSourceSelfSigned,
		Domains: []string{"api.example.com"},
	}
	if err := s.Certificates().Create(ctx, cert); err != nil {
		t.Fatalf("create cert: %v", err)
	}

	h := sampleHost("api", "api.example.com")
	h.CertificateID = &cert.ID
	if err := s.Hosts().Create(ctx, h); err != nil {
		t.Fatalf("create host: %v", err)
	}

	if err := s.Certificates().Delete(ctx, cert.ID); !errors.Is(err, domain.ErrInUse) {
		t.Fatalf("delete in-use cert = %v, want ErrInUse", err)
	}

	n, err := s.Hosts().CountByCertificate(ctx, cert.ID)
	if err != nil {
		t.Fatalf("count by certificate: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}

	if err := s.Hosts().Delete(ctx, h.ID); err != nil {
		t.Fatalf("delete host: %v", err)
	}
	if err := s.Certificates().Delete(ctx, cert.ID); err != nil {
		t.Fatalf("delete cert after host removed: %v", err)
	}
}

func TestMetricsRollupAndPrune(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	repo := s.Metrics()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	samples := make([]domain.Sample, 0, 6)
	for i := range 6 { // 6 samples, 10s apart: one full minute
		sm := domain.Sample{
			HostID:       1,
			Timestamp:    base.Add(time.Duration(i) * 10 * time.Second),
			Requests:     10,
			BytesIn:      100,
			BytesOut:     1000,
			LatencySumMS: 250,
			LatencyMaxMS: uint64(40 + i),
		}
		sm.Status[domain.Status2xx] = 9
		sm.Status[domain.Status5xx] = 1
		samples = append(samples, sm)
	}
	if err := repo.WriteSamples(ctx, samples); err != nil {
		t.Fatalf("write samples: %v", err)
	}

	got, err := repo.Query(ctx, domain.MetricsQuery{
		HostID:     1,
		From:       base,
		To:         base.Add(time.Minute),
		Resolution: domain.ResolutionMinute,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d buckets, want 1", len(got))
	}
	if got[0].Requests != 60 {
		t.Errorf("requests = %d, want 60", got[0].Requests)
	}
	if got[0].Status[domain.Status5xx] != 6 {
		t.Errorf("5xx = %d, want 6", got[0].Status[domain.Status5xx])
	}
	if got[0].LatencyMaxMS != 45 {
		t.Errorf("max latency = %d, want 45", got[0].LatencyMaxMS)
	}
	if mean := got[0].MeanLatencyMS(); mean != 25 {
		t.Errorf("mean latency = %v, want 25", mean)
	}

	// Writing the same interval twice must fold, not fail.
	if err := repo.WriteSamples(ctx, samples[:1]); err != nil {
		t.Fatalf("rewrite sample: %v", err)
	}
	got, err = repo.Query(ctx, domain.MetricsQuery{
		HostID: 1, From: base, To: base.Add(time.Minute), Resolution: domain.ResolutionMinute,
	})
	if err != nil {
		t.Fatalf("query after rewrite: %v", err)
	}
	if got[0].Requests != 70 {
		t.Errorf("requests after fold = %d, want 70", got[0].Requests)
	}

	n, err := repo.Prune(ctx, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 6 {
		t.Errorf("pruned %d rows, want 6", n)
	}
}

func TestPassiveHealthRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	h := sampleHost("api", "api.example.com")
	h.PassiveHealth = domain.PassiveHealth{
		Enabled: true, MaxFails: 7, EjectFor: 90 * time.Second,
	}
	if err := s.Hosts().Create(ctx, h); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.Hosts().Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PassiveHealth != h.PassiveHealth {
		t.Errorf("passive health = %+v, want %+v", got.PassiveHealth, h.PassiveHealth)
	}

	// Switching it off must survive too: false is the value a zero-value
	// bug would silently produce.
	got.PassiveHealth.Enabled = false
	if err := s.Hosts().Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reread, err := s.Hosts().Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if reread.PassiveHealth.Enabled {
		t.Error("disabling passive health did not persist")
	}
}

// TestExistingHostsGainPassiveHealthOnUpgrade covers the migration path: a row
// written before 0002 must come back with the new protection switched on
// rather than with a zero-valued, permanently disabled config.
func TestExistingHostsGainPassiveHealthOnUpgrade(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	// Simulate a pre-0002 row by writing the columns the old schema had and
	// letting the migration's defaults fill the rest.
	if _, err := s.DB().ExecContext(ctx, `
		INSERT INTO hosts (name, enabled, algorithm, created_at, updated_at)
		VALUES ('legacy', 1, 'round_robin', 0, 0)`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	hosts, err := s.Hosts().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("got %d hosts, want 1", len(hosts))
	}
	ph := hosts[0].PassiveHealth
	if !ph.Enabled || ph.MaxFails != 3 || ph.EjectFor != 30*time.Second {
		t.Errorf("legacy host passive health = %+v, want the migration defaults", ph)
	}
}

// TestMigrationOutOfOrderIsRefused covers the trap that parallel development
// sets: if a higher-numbered migration is applied first, the lower one must
// not be silently skipped.
func TestAppliedMigrationsAreTrackedIndividually(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Every embedded migration should now be recorded, not just the last.
	var count int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count < 2 {
		t.Fatalf("recorded %d migrations, want one row per migration", count)
	}

	// Removing a middle row simulates a migration that never ran while a
	// later one did. Reopening must refuse rather than skip it.
	if _, err := s.DB().ExecContext(ctx,
		`DELETE FROM schema_migrations WHERE version = 1`); err != nil {
		t.Fatalf("delete row: %v", err)
	}
	s.Close()

	if _, err := store.Open(ctx, path); err == nil {
		t.Fatal("reopening skipped a missing migration instead of refusing")
	}
}
