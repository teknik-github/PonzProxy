package domain

import (
	"testing"
	"time"
)

func TestNeedsRenewalTracksTheRenewalWindow(t *testing.T) {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cert := &Certificate{
		Source:         CertSourceACME,
		CertificatePEM: "present",
		NotBefore:      issued,
		NotAfter:       issued.Add(90 * 24 * time.Hour),
	}

	// Renewal starts with a third of the lifetime left: 30 of 90 days.
	if cert.NeedsRenewal(issued.Add(50 * 24 * time.Hour)) {
		t.Error("renewal started too early")
	}
	if !cert.NeedsRenewal(issued.Add(65 * 24 * time.Hour)) {
		t.Error("renewal did not start inside the window")
	}
	if !cert.NeedsRenewal(issued.Add(100 * 24 * time.Hour)) {
		t.Error("an expired certificate was not queued for renewal")
	}

	// A requested but not yet issued certificate is always due.
	cert.CertificatePEM = ""
	if !cert.NeedsRenewal(issued) {
		t.Error("an unissued certificate was not queued")
	}

	// Nothing else is ever renewed automatically.
	for _, source := range []CertSource{CertSourceManual, CertSourceSelfSigned} {
		other := &Certificate{
			Source: source, CertificatePEM: "present",
			NotBefore: issued, NotAfter: issued.Add(24 * time.Hour),
		}
		if other.NeedsRenewal(issued.Add(100 * 24 * time.Hour)) {
			t.Errorf("a %s certificate was queued for renewal", source)
		}
	}
}

func TestCertificateValidationBySource(t *testing.T) {
	cases := []struct {
		name  string
		cert  Certificate
		valid bool
	}{
		{
			name:  "acme http-01",
			cert:  Certificate{Name: "a", Domains: []string{"a.example.com"}, Source: CertSourceACME, Challenge: ChallengeHTTP01},
			valid: true,
		},
		{
			name:  "wildcard needs dns-01",
			cert:  Certificate{Name: "a", Domains: []string{"*.example.com"}, Source: CertSourceACME, Challenge: ChallengeTLSALPN01},
			valid: false,
		},
		{
			name:  "wildcard with dns-01 and a provider",
			cert:  Certificate{Name: "a", Domains: []string{"*.example.com"}, Source: CertSourceACME, Challenge: ChallengeDNS01, DNSProvider: "cloudflare"},
			valid: true,
		},
		{
			name:  "dns-01 without a provider",
			cert:  Certificate{Name: "a", Domains: []string{"a.example.com"}, Source: CertSourceACME, Challenge: ChallengeDNS01},
			valid: false,
		},
		{
			name:  "unknown source",
			cert:  Certificate{Name: "a", Domains: []string{"a.example.com"}, Source: "guesswork"},
			valid: false,
		},
		{
			name:  "no domains",
			cert:  Certificate{Name: "a", Source: CertSourceSelfSigned},
			valid: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cert := tc.cert
			cert.Normalize()
			if got := cert.Validate() == nil; got != tc.valid {
				t.Errorf("accepted=%v, want %v (%v)", got, tc.valid, cert.Validate())
			}
		})
	}
}

func TestNormalizeDefaultsACMEChallengeToHTTP01(t *testing.T) {
	cert := Certificate{Name: "a", Domains: []string{"a.example.com"}, Source: CertSourceACME}
	cert.Normalize()
	if cert.Challenge != ChallengeHTTP01 {
		t.Errorf("challenge = %q, want http-01", cert.Challenge)
	}
}

func TestMatchesDomain(t *testing.T) {
	cert := &Certificate{Domains: []string{"api.example.com", "*.internal.test"}}

	cases := map[string]bool{
		"api.example.com":   true,
		"API.Example.com":   true,
		"api.example.com.":  true,
		"other.example.com": false,
		"box.internal.test": true,
		"a.b.internal.test": false, // a wildcard covers one label
		"internal.test":     false, // and never the apex
	}
	for name, want := range cases {
		if got := cert.MatchesDomain(name); got != want {
			t.Errorf("MatchesDomain(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := map[int]StatusClass{
		200: Status2xx, 204: Status2xx,
		301: Status3xx, 404: Status4xx, 500: Status5xx, 503: Status5xx,
		0: StatusError, 99: StatusError,
	}
	for code, want := range cases {
		if got := ClassifyStatus(code); got != want {
			t.Errorf("ClassifyStatus(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestValidateCredentials(t *testing.T) {
	cases := []struct {
		username, password string
		valid              bool
	}{
		{"admin", "long enough password", true},
		{"ad", "long enough password", false},       // username too short
		{"Admin!", "long enough password", false},   // illegal characters
		{"admin", "short", false},                   // under the minimum
		{"admin", string(make([]byte, 100)), false}, // past bcrypt's limit
	}
	for _, tc := range cases {
		if got := ValidateCredentials(tc.username, tc.password) == nil; got != tc.valid {
			t.Errorf("ValidateCredentials(%q, %d bytes) = %v, want %v",
				tc.username, len(tc.password), got, tc.valid)
		}
	}
}
