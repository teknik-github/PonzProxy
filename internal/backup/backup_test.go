package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/store"
)

// fixture builds a data directory that looks like a real one: a database with
// a host in it, a certificate file, and the session secret.
func fixture(t *testing.T) (dir string, db *store.Store) {
	t.Helper()
	dir = t.TempDir()

	db, err := store.Open(context.Background(), filepath.Join(dir, dbName))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	host := &domain.Host{
		Name: "storefront", Enabled: true,
		Domains:   []string{"shop.example.com"},
		Algorithm: domain.RoundRobin,
		Upstreams: []domain.Upstream{{
			Scheme: "http", Address: "127.0.0.1:9101", Weight: 1, Enabled: true,
		}},
	}
	host.Normalize()
	if err := db.Hosts().Create(context.Background(), host); err != nil {
		t.Fatalf("create a host: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "certs"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "certs", "shop.example.com.pem"), "CERTIFICATE MATERIAL")
	write(t, filepath.Join(dir, "certs", "acme_account.key"), "ACME ACCOUNT KEY")
	write(t, filepath.Join(dir, "jwt.secret"), "session-signing-secret")

	return dir, db
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func archive(t *testing.T, dir string, db *store.Store) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Create(context.Background(), db.DB(), dir, "v0.3.0-test", &buf); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return buf.Bytes()
}

func names(t *testing.T, data []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not a gzip: %v", err)
	}
	var out []string
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("read the archive: %v", err)
		}
		out = append(out, h.Name)
	}
}

func TestBackupCarriesEverythingThatCannotBeRebuilt(t *testing.T) {
	dir, db := fixture(t)
	got := names(t, archive(t, dir, db))

	// Each of these is unrecoverable if lost: the configuration would have
	// to be retyped, and the certificates re-issued against a rate limit
	// that allows five per domain per week.
	for _, want := range []string{
		"manifest.json", dbName, "jwt.secret",
		"certs/shop.example.com.pem", "certs/acme_account.key",
	} {
		if !contains(got, want) {
			t.Errorf("the archive is missing %s; it has %v", want, got)
		}
	}
}

func TestBackupIsTakenWhileTheDatabaseIsInUse(t *testing.T) {
	dir, db := fixture(t)
	ctx := context.Background()

	// Writing throughout is the case that matters: a plain file copy of a
	// WAL-mode database under load can be torn, which is why Create uses
	// VACUUM INTO instead.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			h := &domain.Host{
				Name: "busy" + itoa(i), Enabled: true,
				Domains:   []string{"busy" + itoa(i) + ".example.com"},
				Algorithm: domain.RoundRobin,
				Upstreams: []domain.Upstream{{
					Scheme: "http", Address: "127.0.0.1:9000", Weight: 1, Enabled: true,
				}},
			}
			h.Normalize()
			_ = db.Hosts().Create(ctx, h)
		}
	}()

	data := archive(t, dir, db)
	<-done

	restored := restoreInto(t, data)
	hosts := hostsIn(t, restored)
	if len(hosts) == 0 {
		t.Fatal("the snapshot taken under load has no hosts at all")
	}
}

func TestRestoreBringsBackAHostAndItsCertificate(t *testing.T) {
	dir, db := fixture(t)
	data := archive(t, dir, db)

	restored := restoreInto(t, data)

	hosts := hostsIn(t, restored)
	if len(hosts) != 1 || hosts[0].Name != "storefront" {
		t.Fatalf("hosts after restore = %v, want one named storefront", hostNames(hosts))
	}
	if len(hosts[0].Upstreams) != 1 || hosts[0].Upstreams[0].Address != "127.0.0.1:9101" {
		t.Errorf("the host's upstream did not survive: %+v", hosts[0].Upstreams)
	}

	body, err := os.ReadFile(filepath.Join(restored, "certs", "shop.example.com.pem"))
	if err != nil {
		t.Fatalf("the certificate is missing after restore: %v", err)
	}
	if string(body) != "CERTIFICATE MATERIAL" {
		t.Errorf("the certificate came back as %q", body)
	}

	secret, err := os.ReadFile(filepath.Join(restored, "jwt.secret"))
	if err != nil || string(secret) != "session-signing-secret" {
		t.Error("the session secret did not survive, so everyone would be signed out")
	}
}

func TestRestoreMovesTheOldDirectoryAsideRatherThanDeletingIt(t *testing.T) {
	dir, db := fixture(t)
	data := archive(t, dir, db)

	// A second data directory with something recognisable in it, standing
	// in for the installation being restored over.
	target := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(target, "marker"), "the state before the restore")

	moved, err := Restore(target, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if moved == "" {
		t.Fatal("Restore did not say where it put the old directory")
	}

	// Restoring the wrong archive is a mistake people make precisely when
	// they are already having a bad day. Nothing may be deleted.
	body, err := os.ReadFile(filepath.Join(moved, "marker"))
	if err != nil {
		t.Fatalf("the previous data directory was not kept: %v", err)
	}
	if string(body) != "the state before the restore" {
		t.Errorf("the kept directory has %q", body)
	}
}

func TestRestoreRefusesSomethingThatIsNotABackup(t *testing.T) {
	// A gzipped tar that is not ours: unpacking it would scatter files into
	// the data directory.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = writeFile(tw, "manifest.json", []byte(`{"format":"something-else/9"}`), 0o600)
	tw.Close()
	gz.Close()

	target := filepath.Join(t.TempDir(), "data")
	if _, err := Restore(target, bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("an archive in an unknown format was accepted")
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("the refused restore created a data directory anyway")
	}
}

func TestRestoreRefusesAnArchiveWithNoDatabase(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = writeFile(tw, "manifest.json", []byte(`{"format":"`+Format+`"}`), 0o600)
	_ = writeFile(tw, "jwt.secret", []byte("x"), 0o600)
	tw.Close()
	gz.Close()

	if _, err := Restore(filepath.Join(t.TempDir(), "data"), bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("an archive with no database was accepted")
	}
}

func TestRestoreRefusesPathsThatEscape(t *testing.T) {
	for _, name := range []string{"../escaped", "/etc/passwd", "certs/../../escaped"} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		_ = writeFile(tw, "manifest.json", []byte(`{"format":"`+Format+`"}`), 0o600)
		_ = writeFile(tw, name, []byte("owned"), 0o600)
		tw.Close()
		gz.Close()

		// An archive is untrusted input even when an operator supplied it;
		// this is the oldest trick in the tar format.
		if _, err := Restore(filepath.Join(t.TempDir(), "data"), bytes.NewReader(buf.Bytes())); err == nil {
			t.Errorf("an entry named %q was accepted", name)
		}
	}
}

func TestRestoreRefusesASymlink(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = writeFile(tw, "manifest.json", []byte(`{"format":"`+Format+`"}`), 0o600)
	_ = tw.WriteHeader(&tar.Header{
		Name: "certs/evil", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow",
		Mode: 0o777, ModTime: time.Now(),
	})
	tw.Close()
	gz.Close()

	if _, err := Restore(filepath.Join(t.TempDir(), "data"), bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("a symlink in the archive was accepted")
	}
}

func TestDescribeReadsTheManifestWithoutUnpacking(t *testing.T) {
	dir, db := fixture(t)
	data := archive(t, dir, db)

	format, version, created, err := Describe(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if format != Format {
		t.Errorf("format = %q, want %q", format, Format)
	}
	if version != "v0.3.0-test" {
		t.Errorf("version = %q", version)
	}
	if time.Since(created) > time.Minute {
		t.Errorf("createdAt = %v, which is not now", created)
	}
}

func TestScheduledSnapshotsAreKeptToTheLimit(t *testing.T) {
	dir, db := fixture(t)
	s := NewScheduler(db.DB(), dir, "test", time.Hour, 3,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	for range 6 {
		if _, err := s.Snapshot(context.Background()); err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if err := s.prune(); err != nil {
			t.Fatalf("prune: %v", err)
		}
		// The filenames carry a whole-second timestamp, so without this
		// the six snapshots would collide rather than accumulate.
		time.Sleep(1100 * time.Millisecond)
	}

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("kept %d snapshots, want 3", len(got))
	}
	// Newest first, so the console's list needs no sorting of its own.
	for i := 1; i < len(got); i++ {
		if got[i-1].Name < got[i].Name {
			t.Errorf("snapshots are not newest first: %s before %s", got[i-1].Name, got[i].Name)
		}
	}
}

func TestBackupsDoNotContainPreviousBackups(t *testing.T) {
	dir, db := fixture(t)
	s := NewScheduler(db.DB(), dir, "test", time.Hour, 5,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Left unchecked this compounds: every backup would contain every
	// backup before it.
	for _, name := range names(t, archive(t, dir, db)) {
		if strings.HasPrefix(name, "backups/") {
			t.Errorf("the archive contains %s", name)
		}
	}
}

/* ------------------------------------------------------------- helpers -- */

// restoreInto restores an archive into a fresh directory and returns its path.
func restoreInto(t *testing.T, data []byte) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "data")
	if _, err := Restore(target, bytes.NewReader(data)); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return target
}

func hostsIn(t *testing.T, dir string) []domain.Host {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(dir, dbName))
	if err != nil {
		t.Fatalf("open the restored database: %v", err)
	}
	defer db.Close()

	hosts, err := db.Hosts().List(context.Background())
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	return hosts
}

func hostNames(hosts []domain.Host) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.Name
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
