package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

func sampleRedirect(name string, domains ...string) *domain.Redirect {
	rd := &domain.Redirect{
		Name:         name,
		Enabled:      true,
		Domains:      domains,
		Target:       "https://new.example.net",
		StatusCode:   308,
		PreservePath: true,
	}
	rd.Normalize()
	return rd
}

func TestRedirectRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	rd := sampleRedirect("old site", "old.example.com", "www.old.example.com")
	if err := s.Redirects().Create(ctx, rd); err != nil {
		t.Fatalf("create: %v", err)
	}
	if rd.ID == 0 {
		t.Fatal("create did not assign an id")
	}

	got, err := s.Redirects().Get(ctx, rd.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "old site" || got.Target != "https://new.example.net" {
		t.Errorf("unexpected redirect read back: %+v", got)
	}
	if got.StatusCode != 308 || !got.PreservePath || !got.Enabled {
		t.Errorf("flags did not survive the round trip: %+v", got)
	}
	// Order matters: the UI shows the domains in the order they were given.
	if len(got.Domains) != 2 || got.Domains[0] != "old.example.com" {
		t.Errorf("domains = %v, want them in insertion order", got.Domains)
	}
	if got.CertificateID != nil {
		t.Errorf("certificateId = %v, want nil", *got.CertificateID)
	}
}

func TestRedirectListAttachesDomains(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	if err := s.Redirects().Create(ctx, sampleRedirect("b", "b.example.com")); err != nil {
		t.Fatalf("create b: %v", err)
	}
	if err := s.Redirects().Create(ctx, sampleRedirect("a", "a1.example.com", "a2.example.com")); err != nil {
		t.Fatalf("create a: %v", err)
	}

	list, err := s.Redirects().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d redirects, want 2", len(list))
	}
	if list[0].Name != "a" {
		t.Errorf("list is not ordered by name: %q first", list[0].Name)
	}
	if len(list[0].Domains) != 2 || len(list[1].Domains) != 1 {
		t.Errorf("domains not attached per redirect: %v / %v", list[0].Domains, list[1].Domains)
	}
}

func TestRedirectUpdateReplacesDomains(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	rd := sampleRedirect("old site", "old.example.com", "legacy.example.com")
	if err := s.Redirects().Create(ctx, rd); err != nil {
		t.Fatalf("create: %v", err)
	}

	rd.Domains = []string{"old.example.com", "ancient.example.com"}
	rd.StatusCode = 301
	rd.PreservePath = false
	if err := s.Redirects().Update(ctx, rd); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.Redirects().Get(ctx, rd.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Domains) != 2 || got.Domains[1] != "ancient.example.com" {
		t.Errorf("domains = %v, want the set replaced", got.Domains)
	}
	// Keeping a domain it already owned must not trip the uniqueness rule:
	// the old rows are deleted inside the same transaction.
	if got.Domains[0] != "old.example.com" {
		t.Errorf("domains = %v, want the retained one still first", got.Domains)
	}
	if got.StatusCode != 301 || got.PreservePath {
		t.Errorf("flags not updated: %+v", got)
	}
}

func TestRedirectUpdateAndDeleteReportMissing(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	missing := sampleRedirect("gone", "gone.example.com")
	missing.ID = 404
	if err := s.Redirects().Update(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("update of a missing redirect = %v, want ErrNotFound", err)
	}
	if err := s.Redirects().Delete(ctx, 404); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("delete of a missing redirect = %v, want ErrNotFound", err)
	}
	if _, err := s.Redirects().Get(ctx, 404); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get of a missing redirect = %v, want ErrNotFound", err)
	}
}

func TestRedirectDeleteCascadesDomains(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	rd := sampleRedirect("old site", "old.example.com")
	if err := s.Redirects().Create(ctx, rd); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Redirects().Delete(ctx, rd.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// The domain must be free again, which only holds if the child rows
	// went with the parent.
	if err := s.Redirects().Create(ctx, sampleRedirect("reused", "old.example.com")); err != nil {
		t.Fatalf("recreate on the freed domain: %v", err)
	}
}

func TestRedirectDomainIsUniqueAcrossRedirects(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	if err := s.Redirects().Create(ctx, sampleRedirect("first", "old.example.com")); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := s.Redirects().Create(ctx, sampleRedirect("second", "old.example.com"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second redirect on the same domain = %v, want ErrConflict", err)
	}
}

// Hosts and redirects share one domain namespace: a request carries a single
// Host header, so a domain that is both proxied and redirected would have no
// defined answer. The schema enforces it in both directions.
func TestRedirectAndHostCannotShareADomain(t *testing.T) {
	ctx := context.Background()

	t.Run("redirect over a host domain", func(t *testing.T) {
		s := open(t)
		if err := s.Hosts().Create(ctx, sampleHost("api", "shared.example.com")); err != nil {
			t.Fatalf("create host: %v", err)
		}
		err := s.Redirects().Create(ctx, sampleRedirect("clash", "shared.example.com"))
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("redirect over a host's domain = %v, want ErrConflict", err)
		}
	})

	t.Run("host over a redirect domain", func(t *testing.T) {
		s := open(t)
		if err := s.Redirects().Create(ctx, sampleRedirect("old", "shared.example.com")); err != nil {
			t.Fatalf("create redirect: %v", err)
		}
		// The host repository maps this failure to its own message, so
		// only the refusal itself is asserted here.
		if err := s.Hosts().Create(ctx, sampleHost("api", "shared.example.com")); err == nil {
			t.Fatal("a host took over a redirect's domain")
		}
	})

	t.Run("a rejected write leaves nothing behind", func(t *testing.T) {
		s := open(t)
		if err := s.Hosts().Create(ctx, sampleHost("api", "shared.example.com")); err != nil {
			t.Fatalf("create host: %v", err)
		}
		rd := sampleRedirect("clash", "free.example.com", "shared.example.com")
		if err := s.Redirects().Create(ctx, rd); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("create = %v, want ErrConflict", err)
		}
		list, err := s.Redirects().List(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 0 {
			t.Errorf("got %d redirects after a rejected create, want 0", len(list))
		}
		// The first domain must not have been left claimed by the rolled
		// back transaction.
		if err := s.Redirects().Create(ctx, sampleRedirect("clean", "free.example.com")); err != nil {
			t.Errorf("the rolled back domain is still claimed: %v", err)
		}
	})
}

func TestRedirectStatusCodeIsConstrainedBySchema(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	// The domain type rejects this first; the CHECK is the backstop for any
	// future writer that forgets to call Validate.
	rd := sampleRedirect("bad", "old.example.com")
	rd.StatusCode = 303
	if err := s.Redirects().Create(ctx, rd); err == nil {
		t.Fatal("the schema accepted a 303 redirect")
	}
}

func TestRedirectCountByCertificate(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	cert := &domain.Certificate{
		Name:    "wildcard",
		Domains: []string{"*.example.com"},
		Source:  domain.CertSourceSelfSigned,
	}
	cert.Normalize()
	if err := s.Certificates().Create(ctx, cert); err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	if n, err := s.Redirects().CountByCertificate(ctx, cert.ID); err != nil || n != 0 {
		t.Fatalf("count before = %d (%v), want 0", n, err)
	}

	rd := sampleRedirect("old site", "old.example.com")
	rd.CertificateID = &cert.ID
	if err := s.Redirects().Create(ctx, rd); err != nil {
		t.Fatalf("create redirect: %v", err)
	}

	if n, err := s.Redirects().CountByCertificate(ctx, cert.ID); err != nil || n != 1 {
		t.Errorf("count after = %d (%v), want 1", n, err)
	}
	// ON DELETE RESTRICT: a certificate a redirect is serving must not be
	// able to vanish from under the listener.
	if err := s.Certificates().Delete(ctx, cert.ID); err == nil {
		t.Error("a certificate in use by a redirect was deleted")
	}
}
