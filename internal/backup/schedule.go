package backup

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/platform/logging"
)

// Scheduler keeps a rolling set of local snapshots.
//
// A local copy protects against the mistakes an operator actually makes — a
// host deleted, a certificate replaced with the wrong one, a database
// corrupted — and against nothing else. It sits on the same disk, so it does
// not survive that disk. Saying so is the point of this comment and of the
// wording on the console: a scheduled snapshot that someone mistakes for
// off-site protection is worse than no snapshot at all.
type Scheduler struct {
	db      *sql.DB
	dataDir string
	version string
	logger  *slog.Logger

	every time.Duration
	keep  int
}

// Dir is where snapshots are written. It sits inside the data directory, which
// is safe because Create names the paths it copies rather than walking the
// directory: a backup can never come to contain the previous backups.
func Dir(dataDir string) string { return filepath.Join(dataDir, "backups") }

// NewScheduler builds a scheduler. An interval of zero disables it.
func NewScheduler(db *sql.DB, dataDir, version string, every time.Duration, keep int, logger *slog.Logger) *Scheduler {
	if keep < 1 {
		keep = 1
	}
	return &Scheduler{
		db: db, dataDir: dataDir, version: version,
		logger: logger.With("component", "backup"),
		every:  every, keep: keep,
	}
}

// Run takes a snapshot on the interval until ctx is cancelled.
//
// The first one is taken at startup rather than after a full interval: a proxy
// restarted daily would otherwise never produce a backup at all, and the case
// where someone most wants yesterday's copy is the one where something has
// been restarting.
func (s *Scheduler) Run(ctx context.Context) {
	if s.every <= 0 {
		s.logger.Info("scheduled backups are off")
		return
	}

	s.once(ctx)

	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.once(ctx)
		}
	}
}

func (s *Scheduler) once(ctx context.Context) {
	path, err := s.Snapshot(ctx)
	if err != nil {
		if !logging.IsShutdown(err) {
			// A failed backup must never take the proxy down, but it is
			// the kind of quiet failure that is only noticed when it is
			// too late, so it is logged at error level.
			s.logger.Error("could not write a backup", "error", err)
		}
		return
	}
	info, statErr := os.Stat(path)
	size := int64(0)
	if statErr == nil {
		size = info.Size()
	}
	s.logger.Info("wrote a backup", "file", filepath.Base(path), "bytes", size)

	if err := s.prune(); err != nil {
		s.logger.Error("could not prune old backups", "error", err)
	}
}

// Snapshot writes one archive into the backup directory and returns its path.
func (s *Scheduler) Snapshot(ctx context.Context) (string, error) {
	dir := Dir(s.dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the backup directory: %w", err)
	}

	name := fmt.Sprintf("ponzproxy-%s.tar.gz", time.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(dir, name)

	// Written under a temporary name and renamed into place, so a crash
	// half way through leaves a .partial file rather than a truncated
	// archive that looks like a backup.
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}

	if err := Create(ctx, s.db, s.dataDir, s.version, f); err != nil {
		f.Close()
		os.Remove(partial)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(partial)
		return "", err
	}
	if err := os.Rename(partial, path); err != nil {
		os.Remove(partial)
		return "", err
	}
	return path, nil
}

// prune keeps the newest `keep` archives and removes the rest, along with any
// .partial left behind by an interrupted run.
func (s *Scheduler) prune() error {
	dir := Dir(s.dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch {
		case strings.HasSuffix(e.Name(), ".partial"):
			os.Remove(filepath.Join(dir, e.Name()))
		case strings.HasPrefix(e.Name(), "ponzproxy-") && strings.HasSuffix(e.Name(), ".tar.gz"):
			names = append(names, e.Name())
		}
	}

	// The names carry a sortable timestamp, so lexical order is
	// chronological order and no stat calls are needed.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names[min(s.keep, len(names)):] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot describes one archive on disk, for the console's list.
type Snapshot struct {
	Name      string    `json:"name"`
	Bytes     int64     `json:"bytes"`
	CreatedAt time.Time `json:"createdAt"`
}

// List returns the snapshots on disk, newest first.
func List(dataDir string) ([]Snapshot, error) {
	entries, err := os.ReadDir(Dir(dataDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	out := make([]Snapshot, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "ponzproxy-") ||
			!strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Snapshot{
			Name: e.Name(), Bytes: info.Size(), CreatedAt: info.ModTime().UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}
