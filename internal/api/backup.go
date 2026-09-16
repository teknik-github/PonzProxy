package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/backup"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// Backups are administrator-only, on every route below, because the archive
// contains every private key the proxy holds, the ACME account key and the
// secret that signs sessions. Whoever can download one can impersonate every
// site this proxy serves. A viewer must not be able to.

type backupSettings struct {
	// EverySeconds is 0 when scheduled snapshots are off.
	EverySeconds int               `json:"everySeconds"`
	Keep         int               `json:"keep"`
	Directory    string            `json:"directory"`
	Snapshots    []backup.Snapshot `json:"snapshots"`
}

// handleBackupStatus lists the snapshots on disk and the schedule that wrote
// them, so an operator can see at a glance whether backups are actually
// happening rather than assuming they are.
func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	snapshots, err := backup.List(s.opts.DataDir)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if snapshots == nil {
		snapshots = []backup.Snapshot{}
	}
	writeJSON(w, s.logger, http.StatusOK, backupSettings{
		EverySeconds: int(s.opts.BackupEvery.Seconds()),
		Keep:         s.opts.BackupKeep,
		Directory:    backup.Dir(s.opts.DataDir),
		Snapshots:    snapshots,
	})
}

// handleBackupDownload streams a fresh archive.
//
// Fresh rather than the newest file on disk: someone clicking Download
// immediately before an upgrade means "give me the state as it is now", and
// handing them a copy that could be a day old would be the wrong answer at the
// exact moment it mattered.
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	if s.opts.DB == nil {
		writeError(w, s.logger, errors.New("backups are not configured on this server"))
		return
	}

	name := fmt.Sprintf("ponzproxy-%s.tar.gz", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")

	// Streamed, so a large database is not held in memory twice. The cost
	// is that a failure part way through cannot become a clean 500 — the
	// client has a truncated file. It is logged loudly for that reason, and
	// the restore path refuses an archive with no manifest, which is what a
	// truncated one looks like.
	if err := backup.Create(r.Context(), s.opts.DB, s.opts.DataDir, s.opts.Version, w); err != nil {
		s.logger.Error("backup download failed part way through", "error", err)
	}
}

// handleBackupCreate writes a snapshot to disk on demand, for an operator who
// wants one before making a change rather than waiting for the schedule.
func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	if s.opts.Snapshot == nil {
		writeError(w, s.logger, errors.New("backups are not configured on this server"))
		return
	}
	path, err := s.opts.Snapshot(r.Context())
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	s.logger.Info("backup written on request", "file", filepath.Base(path))
	writeJSON(w, s.logger, http.StatusCreated, backup.Snapshot{
		Name: filepath.Base(path), Bytes: info.Size(), CreatedAt: info.ModTime().UTC(),
	})
}

// handleBackupFile serves one snapshot already on disk by name.
func (s *Server) handleBackupFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// The name comes from a URL. Anything with a separator in it is either
	// a mistake or an attempt to read a file outside the backup directory.
	if name == "" || name != filepath.Base(name) || filepath.IsAbs(name) {
		writeError(w, s.logger, errors.Join(errBadRequest, errors.New("invalid backup name")))
		return
	}

	path := filepath.Join(backup.Dir(s.opts.DataDir), name)
	f, err := os.Open(path)
	if err != nil {
		writeError(w, s.logger, domain.ErrNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeError(w, s.logger, domain.ErrNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// handleBackupDelete removes one snapshot.
func (s *Server) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || name != filepath.Base(name) || filepath.IsAbs(name) {
		writeError(w, s.logger, errors.Join(errBadRequest, errors.New("invalid backup name")))
		return
	}
	if err := os.Remove(filepath.Join(backup.Dir(s.opts.DataDir), name)); err != nil {
		writeError(w, s.logger, domain.ErrNotFound)
		return
	}
	s.logger.Info("backup deleted", "file", name)
	writeJSON(w, s.logger, http.StatusNoContent, nil)
}
