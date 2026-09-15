package domain

import (
	"net/url"
	"strings"
	"testing"
)

func workingRedirect() *Redirect {
	r := &Redirect{
		Name:         "old site",
		Enabled:      true,
		Domains:      []string{"old.example.com"},
		Target:       "https://new.example.com",
		StatusCode:   301,
		PreservePath: true,
	}
	r.Normalize()
	return r
}

func TestRedirectNormalizeCanonicalisesDomainsAndTarget(t *testing.T) {
	r := &Redirect{
		Name:    "  old site  ",
		Domains: []string{" OLD.Example.COM. ", "old.example.com", "", "www.old.example.com"},
		Target:  "  NEW.Example.com/Moved  ",
	}
	r.Normalize()

	if r.Name != "old site" {
		t.Errorf("name = %q, want it trimmed", r.Name)
	}
	if len(r.Domains) != 2 || r.Domains[0] != "old.example.com" {
		t.Errorf("domains = %v, want two canonical entries", r.Domains)
	}
	// The host is lowercased but the path is not: paths are case-sensitive.
	if r.Target != "https://new.example.com/Moved" {
		t.Errorf("target = %q, want the host lowered and https assumed", r.Target)
	}
	if r.StatusCode != DefaultRedirectStatus {
		t.Errorf("statusCode = %d, want the %d default", r.StatusCode, DefaultRedirectStatus)
	}

	// Normalize must be safe to run twice.
	before := r.Target
	r.Normalize()
	if r.Target != before {
		t.Errorf("second Normalize changed the target: %q -> %q", before, r.Target)
	}
}

func TestRedirectNormalizeTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"bare domain gets https", "example.com", "https://example.com"},
		{"bare domain with path", "example.com/a/b", "https://example.com/a/b"},
		{"https kept", "https://example.com/x", "https://example.com/x"},
		{"http kept, not upgraded", "http://example.com", "http://example.com"},
		{"scheme lowercased", "HTTPS://Example.COM/A", "https://example.com/A"},
		{"protocol relative resolved to https", "//example.com/a", "https://example.com/a"},
		{"host:port kept", "example.com:8443/a", "https://example.com:8443/a"},
		{"query kept", "example.com/s?q=1", "https://example.com/s?q=1"},
		{"empty stays empty", "   ", ""},
		// A scheme ponzproxy will not emit survives Normalize unchanged so
		// that Validate is the one place that rejects it.
		{"foreign scheme left for Validate", "javascript:alert(1)", "javascript:alert(1)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeTarget(tc.in); got != tc.want {
				t.Errorf("normalizeTarget(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedirectValidateAcceptsEveryAllowedStatus(t *testing.T) {
	for _, code := range RedirectStatuses() {
		r := workingRedirect()
		r.StatusCode = code
		if err := r.Validate(); err != nil {
			t.Errorf("status %d was rejected: %v", code, err)
		}
	}
}

func TestRedirectValidateRejectsOtherStatuses(t *testing.T) {
	// 303 and 300 are real redirect codes that ponzproxy still refuses: 303
	// always downgrades to GET, and 300 needs a body to be useful. 200 and
	// 0 stand in for "not a redirect at all".
	for _, code := range []int{0, 200, 300, 303, 304, 399, 418, 999, -301} {
		r := workingRedirect()
		r.StatusCode = code
		err := r.Validate()
		if err == nil {
			t.Errorf("status %d was accepted", code)
			continue
		}
		if _, ok := fieldsOf(t, err)["statusCode"]; !ok {
			t.Errorf("status %d: error does not name the statusCode field: %v", code, err)
		}
	}
}

func TestRedirectValidateRejectsBadTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
	}{
		// A javascript: or data: Location is executed by some clients, so
		// a stored redirect must never be able to carry one.
		{"javascript scheme", "javascript:alert(1)"},
		{"data scheme", "data:text/html,<script>alert(1)</script>"},
		{"file scheme", "file:///etc/passwd"},
		{"no host", "https:///just/a/path"},
		{"fragment", "https://new.example.com/#section"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := workingRedirect()
			r.Target = tc.target
			r.Normalize()

			err := r.Validate()
			if err == nil {
				t.Fatalf("target %q was accepted, normalised to %q", tc.target, r.Target)
			}
			if _, ok := fieldsOf(t, err)["target"]; !ok {
				t.Errorf("error does not name the target field: %v", err)
			}
		})
	}
}

func TestRedirectValidateRejectsALoop(t *testing.T) {
	for _, tc := range []struct{ name, domain, target string }{
		{"exact", "old.example.com", "https://old.example.com/new"},
		{"wildcard covers the target", "*.example.com", "https://www.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := workingRedirect()
			r.Domains = []string{tc.domain}
			r.Target = tc.target
			r.Normalize()

			if err := r.Validate(); err == nil {
				t.Fatal("a redirect pointing at its own domain was accepted")
			} else if msg := fieldsOf(t, err)["target"]; !strings.Contains(msg, "points back") {
				t.Errorf("target message = %q, want it to explain the loop", msg)
			}
		})
	}
}

func TestRedirectValidateNamesEveryBadField(t *testing.T) {
	r := &Redirect{StatusCode: 302, Domains: []string{"not a domain"}}
	r.Normalize()

	fields := fieldsOf(t, r.Validate())
	for _, want := range []string{"name", "domains[0]", "target"} {
		if _, ok := fields[want]; !ok {
			t.Errorf("no error reported for %q; got %v", want, fields)
		}
	}
}

func TestRedirectLocation(t *testing.T) {
	for _, tc := range []struct {
		name         string
		target       string
		preservePath bool
		request      string
		want         string
	}{
		{
			name:    "path dropped when preservation is off",
			target:  "https://new.example.com",
			request: "/a/b?c=d",
			want:    "https://new.example.com",
		},
		{
			name:    "the target's own path and query survive with preservation off",
			target:  "https://new.example.com/landing?utm_source=old",
			request: "/a/b?c=d",
			want:    "https://new.example.com/landing?utm_source=old",
		},
		{
			name:         "path and query appended",
			target:       "https://new.example.com",
			preservePath: true,
			request:      "/a/b?c=d",
			want:         "https://new.example.com/a/b?c=d",
		},
		{
			name:         "root request keeps the bare target",
			target:       "https://new.example.com",
			preservePath: true,
			request:      "/",
			want:         "https://new.example.com/",
		},
		{
			name:         "target path becomes a prefix",
			target:       "https://new.example.com/app",
			preservePath: true,
			request:      "/a/b",
			want:         "https://new.example.com/app/a/b",
		},
		{
			name:         "a trailing slash on the target does not double up",
			target:       "https://new.example.com/app/",
			preservePath: true,
			request:      "/a/b",
			want:         "https://new.example.com/app/a/b",
		},
		{
			name:         "a prefixed target is not left with a stray trailing slash",
			target:       "https://new.example.com/app/",
			preservePath: true,
			request:      "/",
			want:         "https://new.example.com/app",
		},
		{
			name:         "both queries are kept",
			target:       "https://new.example.com/?utm_source=old",
			preservePath: true,
			request:      "/page?id=7",
			want:         "https://new.example.com/page?utm_source=old&id=7",
		},
		{
			name:         "an empty query is not turned into a bare question mark",
			target:       "https://new.example.com",
			preservePath: true,
			request:      "/page?",
			want:         "https://new.example.com/page",
		},
		{
			name:         "percent encoding in the path is preserved verbatim",
			target:       "https://new.example.com",
			preservePath: true,
			request:      "/a%2Fb/c%20d?q=a%26b",
			want:         "https://new.example.com/a%2Fb/c%20d?q=a%26b",
		},
		{
			// The request cannot move the visitor to another site: the
			// scheme and authority always come from the target, so a path
			// that looks like an authority stays a path.
			name:         "an authority-shaped path cannot escape the target host",
			target:       "https://new.example.com",
			preservePath: true,
			request:      "//evil.example/takeover",
			want:         "https://new.example.com//evil.example/takeover",
		},
		{
			name:         "a port on the target is kept",
			target:       "https://new.example.com:8443",
			preservePath: true,
			request:      "/a",
			want:         "https://new.example.com:8443/a",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Redirect{
				Name:         "r",
				Domains:      []string{"old.example.com"},
				Target:       tc.target,
				StatusCode:   308,
				PreservePath: tc.preservePath,
			}
			r.Normalize()
			if err := r.Validate(); err != nil {
				t.Fatalf("fixture does not validate: %v", err)
			}

			// ParseRequestURI, not Parse: it is what net/http uses on an
			// origin-form request line, and it is the difference between
			// "//evil.example/x" being a path and being an authority.
			reqURL, err := url.ParseRequestURI(tc.request)
			if err != nil {
				t.Fatalf("parse request %q: %v", tc.request, err)
			}
			if got := r.Location(reqURL); got != tc.want {
				t.Errorf("Location(%q) = %q, want %q", tc.request, got, tc.want)
			}
		})
	}
}

// A Location header carrying a raw CR or LF would let a request split the
// response. net/http also guards this, but the redirect is the one place a
// request's own bytes are echoed into a header, so it is checked here too.
func TestRedirectLocationStripsControlCharacters(t *testing.T) {
	r := workingRedirect()
	got := r.Location(&url.URL{Path: "/a\r\nX-Injected: 1/b"})

	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("Location = %q, want no line breaks", got)
	}
	if !strings.HasPrefix(got, "https://new.example.com/a") {
		t.Errorf("Location = %q, want it still rooted at the target", got)
	}
}

func TestRedirectMatchesDomain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		domains []string
		host    string
		want    bool
	}{
		{"exact", []string{"old.example.com"}, "old.example.com", true},
		{"exact is case insensitive", []string{"old.example.com"}, "OLD.Example.COM", true},
		{"trailing dot", []string{"old.example.com"}, "old.example.com.", true},
		{"other domain", []string{"old.example.com"}, "new.example.com", false},
		{"wildcard covers one label", []string{"*.example.com"}, "shop.example.com", true},
		// One label only, matching how TLS wildcards work.
		{"wildcard does not cover two labels", []string{"*.example.com"}, "a.b.example.com", false},
		{"wildcard does not cover the parent", []string{"*.example.com"}, "example.com", false},
		{"wildcard does not cover a sibling zone", []string{"*.example.com"}, "shop.example.net", false},
		{"second domain in the list", []string{"a.example.com", "b.example.com"}, "b.example.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Redirect{Domains: tc.domains}
			if got := r.MatchesDomain(tc.host); got != tc.want {
				t.Errorf("MatchesDomain(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestRedirectWildcardDomainValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		domain  string
		wantErr bool
	}{
		{"leftmost wildcard", "*.example.com", false},
		{"wildcard needs a registrable domain", "*.com", true},
		{"wildcard must be leftmost", "a.*.example.com", true},
		{"partial wildcard label", "*x.example.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := workingRedirect()
			r.Domains = []string{tc.domain}
			// A target outside every candidate domain, so only the
			// wildcard rule is under test here.
			r.Target = "https://elsewhere.example.net"
			r.Normalize()

			err := r.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("domain %q was accepted", tc.domain)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("domain %q was rejected: %v", tc.domain, err)
			}
		})
	}
}
