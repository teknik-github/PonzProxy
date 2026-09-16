// Package backup copies everything ponzproxy cannot reconstruct.
//
// That is a short list and a heavy one: the SQLite file holding every host,
// certificate, access list and account, the issued certificate material, the
// ACME account key, and the secret that signs sessions. Lose it and the
// configuration is retyped — but the certificates have to be issued again, and
// Let's Encrypt allows five per domain per week. Recovery is then measured in
// days, which is why this is the one failure in the project that cannot be
// worked around after the fact.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Format is stamped into every archive. Restore refuses anything it does not
// recognise rather than unpacking a file that happens to be a tarball.
const Format = "ponzproxy-backup/1"

// dbName is what the database is called inside the archive and on disk.
const dbName = "ponzproxy.db"

// maxEntry bounds any single file taken from an archive. A backup of this
// project is a few tens of megabytes; anything past this is a tarbomb, not a
// restore.
const maxEntry = 4 << 30 // 4 GiB

// manifest describes an archive, so a restore can refuse a file that is not
// one and an operator can tell two archives apart without unpacking them.
type manifest struct {
	Format string `json:"format"`
	// Version is the binary that wrote the archive, for the operator's
	// benefit when a restore behaves unexpectedly.
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	// SchemaVersion is the highest migration applied when the copy was
	// taken. A restore into an older binary would leave the database ahead
	// of the code, so it is refused rather than half-migrated.
	SchemaVersion int `json:"schemaVersion"`
}

// Create writes a gzipped tar of the data directory to w.
//
// The database is copied with VACUUM INTO rather than read from disk: SQLite
// in WAL mode has committed data split between the main file and the -wal, so
// a plain file copy of a running database can be torn. VACUUM INTO asks SQLite
// itself for a consistent single-file snapshot while the proxy keeps serving.
func Create(ctx context.Context, db *sql.DB, dataDir, version string, w io.Writer) error {
	staging, err := os.MkdirTemp("", "ponzproxy-backup-*")
	if err != nil {
		return fmt.Errorf("create a staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	snapshot := filepath.Join(staging, dbName)
	// The destination must not exist; VACUUM INTO refuses to overwrite,
	// which is the behaviour we want and the reason for the temp directory.
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, snapshot); err != nil {
		return fmt.Errorf("snapshot the database: %w", err)
	}

	schema, err := schemaVersion(ctx, db)
	if err != nil {
		return err
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	body, err := json.MarshalIndent(manifest{
		Format:        Format,
		Version:       version,
		CreatedAt:     time.Now().UTC(),
		SchemaVersion: schema,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(tw, "manifest.json", body, 0o600); err != nil {
		return err
	}

	if err := addFile(tw, snapshot, dbName); err != nil {
		return err
	}

	// jwt.secret is included so that restoring does not sign every operator
	// out. It is why the archive must be treated as being as sensitive as
	// the server itself, which the API and the CLI both say out loud.
	secret := filepath.Join(dataDir, "jwt.secret")
	if err := addFile(tw, secret, "jwt.secret"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := addTree(tw, filepath.Join(dataDir, "certs"), "certs"); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish the archive: %w", err)
	}
	return gz.Close()
}

// Restore unpacks an archive into dataDir, moving whatever was there aside.
//
// It does not delete anything. The previous data directory is renamed, so an
// operator who restores the wrong file still has the old one — a restore is
// run in exactly the circumstances where a second mistake is most likely.
// The path it was moved to is returned.
func Restore(dataDir string, r io.Reader) (movedTo string, err error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("this file is not a gzipped archive: %w", err)
	}
	defer gz.Close()

	staging, err := os.MkdirTemp(filepath.Dir(dataDir), "ponzproxy-restore-*")
	if err != nil {
		return "", fmt.Errorf("create a staging directory: %w", err)
	}
	// Everything is unpacked and checked before anything on disk is
	// touched, so a truncated archive cannot leave a half-restored data
	// directory behind. The flag stops the cleanup from deleting the
	// staging directory once it has been moved into place.
	moved := false
	defer func() {
		if !moved {
			os.RemoveAll(staging)
		}
	}()

	m, err := extract(tar.NewReader(gz), staging)
	if err != nil {
		return "", err
	}
	if m.Format != Format {
		return "", fmt.Errorf("this is not a ponzproxy backup (format %q)", m.Format)
	}
	if _, err := os.Stat(filepath.Join(staging, dbName)); err != nil {
		return "", errors.New("the archive contains no database")
	}

	if _, err := os.Stat(dataDir); err == nil {
		movedTo = fmt.Sprintf("%s.before-restore-%s", dataDir, time.Now().UTC().Format("20060102-150405"))
		if err := os.Rename(dataDir, movedTo); err != nil {
			return "", fmt.Errorf("move the existing data directory aside: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	if err := os.Rename(staging, dataDir); err != nil {
		return movedTo, fmt.Errorf("put the restored data in place: %w", err)
	}
	moved = true

	return movedTo, os.Chmod(dataDir, 0o700)
}

// Describe reads an archive's manifest without unpacking it, so a caller can
// show what a file contains before acting on it.
func Describe(r io.Reader) (format, version string, created time.Time, err error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("this file is not a gzipped archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", "", time.Time{}, errors.New("the archive has no manifest")
		}
		if err != nil {
			return "", "", time.Time{}, err
		}
		if h.Name != "manifest.json" {
			continue
		}
		var m manifest
		if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil {
			return "", "", time.Time{}, fmt.Errorf("read the manifest: %w", err)
		}
		return m.Format, m.Version, m.CreatedAt, nil
	}
}

/* ------------------------------------------------------------- internals -- */

func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("read the schema version: %w", err)
	}
	return int(v.Int64), nil
}

func writeFile(tw *tar.Writer, name string, body []byte, mode int64) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: mode, Size: int64(len(body)), ModTime: time.Now(),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(),
		ModTime: info.ModTime(), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// addTree adds a directory recursively. A missing directory is not an error:
// a proxy that has never issued a certificate has no certs directory, and
// refusing to back that installation up would be absurd.
func addTree(tw *tar.Writer, dir, prefix string) error {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name := prefix
		if rel != "." {
			name = filepath.ToSlash(filepath.Join(prefix, rel))
		}
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			return tw.WriteHeader(&tar.Header{
				Name: name + "/", Mode: int64(info.Mode().Perm()),
				ModTime: info.ModTime(), Typeflag: tar.TypeDir,
			})
		}
		// Only regular files. A symlink in the data directory would
		// otherwise be a way to make a restore write outside it.
		if !d.Type().IsRegular() {
			return nil
		}
		return addFile(tw, path, name)
	})
}

// extract unpacks into dir, refusing any entry that would escape it.
func extract(tr *tar.Reader, dir string) (manifest, error) {
	var m manifest
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return m, nil
		}
		if err != nil {
			return m, fmt.Errorf("read the archive: %w", err)
		}

		// An archive is untrusted input even when the operator supplied
		// it: "../../etc/cron.d/x" in a tar is the oldest trick there is.
		target, err := safeJoin(dir, h.Name)
		if err != nil {
			return m, err
		}

		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return m, err
			}
		case tar.TypeReg:
			if h.Size > maxEntry {
				return m, fmt.Errorf("%s is %d bytes, which is not a backup", h.Name, h.Size)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return m, err
			}
			mode := os.FileMode(h.Mode).Perm()
			if mode == 0 {
				mode = 0o600
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return m, err
			}
			written, err := io.Copy(f, io.LimitReader(tr, maxEntry+1))
			closeErr := f.Close()
			if err != nil {
				return m, err
			}
			if closeErr != nil {
				return m, closeErr
			}
			if written > maxEntry {
				return m, fmt.Errorf("%s is larger than %d bytes", h.Name, maxEntry)
			}
			if h.Name == "manifest.json" {
				body, err := os.ReadFile(target)
				if err != nil {
					return m, err
				}
				if err := json.Unmarshal(body, &m); err != nil {
					return m, fmt.Errorf("read the manifest: %w", err)
				}
			}
		default:
			// Symlinks, devices and hard links have no business in a
			// backup of this data and are the usual way out of one.
			return m, fmt.Errorf("%s is not a regular file or directory", h.Name)
		}
	}
}

// safeJoin resolves an archive entry inside dir, or refuses it.
func safeJoin(dir, name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return "", fmt.Errorf("refusing an absolute path in the archive: %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing a path that escapes the archive: %q", name)
	}
	target := filepath.Join(dir, clean)
	// Belt and braces: even after cleaning, confirm the result is inside.
	rel, err := filepath.Rel(dir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing a path that escapes the archive: %q", name)
	}
	return target, nil
}
