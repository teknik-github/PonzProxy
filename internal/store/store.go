// Package store implements the domain repositories on top of SQLite. It is
// the only package in ponzproxy that knows SQL exists.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, so the binary needs no CGO

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store owns the database handle and exposes one repository per aggregate.
type Store struct {
	db *sql.DB
}

// Open connects to the SQLite file at path and brings the schema up to date.
//
// The pragmas matter: WAL lets the metrics writer commit while the API reads,
// busy_timeout turns lock contention into a short wait instead of an
// immediate SQLITE_BUSY, and foreign_keys is off by default in SQLite so the
// ON DELETE clauses in the schema would otherwise be decorative.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite serialises writes anyway; a small pool avoids pointless lock
	// contention while still allowing concurrent readers under WAL.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for health checks and diagnostics. Repositories should
// be preferred everywhere else.
func (s *Store) DB() *sql.DB { return s.db }

// Hosts returns the host repository.
func (s *Store) Hosts() domain.HostRepository { return (*hostRepo)(s) }

// Certificates returns the certificate repository.
func (s *Store) Certificates() domain.CertificateRepository { return (*certRepo)(s) }

// Users returns the user repository.
func (s *Store) Users() domain.UserRepository { return (*userRepo)(s) }

// Metrics returns the metrics repository.
func (s *Store) Metrics() domain.MetricsRepository { return (*metricsRepo)(s) }

// migrate applies every embedded migration whose version is above the one
// recorded in schema_migrations, each inside its own transaction so a failure
// leaves the database at the last complete version.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// Every applied version is tracked, not just the highest. Taking the
	// maximum would silently skip a migration that lands out of order —
	// which is exactly what happens when two branches are developed in
	// parallel and the lower number merges second.
	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	var highest int
	for v := range applied {
		if v > highest {
			highest = v
		}
	}

	for _, m := range migrations {
		if _, done := applied[m.version]; done {
			continue
		}
		// Refusing is better than applying out of order: a migration
		// written against an older schema may not hold against the newer
		// one, and silently skipping it would leave the database missing
		// a change nothing ever reports.
		if m.version < highest {
			return fmt.Errorf(
				"migration %04d (%s) has not been applied but %04d already has; "+
					"the database is ahead of it. Renumber it above %04d and re-run",
				m.version, m.name, highest, highest)
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("migration %04d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

// appliedVersions reads the set of migrations this database has already run.
func (s *Store) appliedVersions(ctx context.Context) (map[int]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]struct{})
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = struct{}{}
	}
	return applied, rows.Err()
}

type migration struct {
	version int
	name    string
	sql     string
}

func (s *Store) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// loadMigrations reads the embedded .sql files, which are named
// "NNNN_description.sql" and applied in ascending numeric order.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, ok := parseMigrationName(e.Name())
		if !ok {
			return nil, fmt.Errorf("migration %q does not match NNNN_name.sql", e.Name())
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d", out[i].version)
		}
	}
	return out, nil
}

func parseMigrationName(filename string) (version int, name string, ok bool) {
	base := strings.TrimSuffix(filename, ".sql")
	prefix, rest, found := strings.Cut(base, "_")
	if !found || len(prefix) == 0 {
		return 0, "", false
	}
	for _, r := range prefix {
		if r < '0' || r > '9' {
			return 0, "", false
		}
		version = version*10 + int(r-'0')
	}
	return version, rest, true
}

// withTx runs fn inside a transaction, rolling back on any error. Repositories
// use it for every multi-table write so a host is never half-saved.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// translateErr maps driver-specific constraint failures onto the sentinel
// errors the rest of the module matches on, so no caller imports the driver.
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return fmt.Errorf("%w: %s", domain.ErrConflict, msg)
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		return fmt.Errorf("%w: referenced record is missing or still in use", domain.ErrInUse)
	}
	return err
}

// nullTime converts a nullable unix timestamp column into a *time.Time.
func nullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}

// timePtrToNull is the inverse of nullTime.
func timePtrToNull(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

// errorIsConflict reports whether err came back from translateErr as a
// uniqueness violation.
func errorIsConflict(err error) bool { return errors.Is(err, domain.ErrConflict) }
