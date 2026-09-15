package domain

import (
	"errors"
	"strings"
	"testing"
)

// fieldsOf collects the field names a validation error reports, which is what
// the UI uses to mark inputs.
func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var v *ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error is not a ValidationError: %v", err)
	}
	out := make(map[string]string, len(v.Fields))
	for _, f := range v.Fields {
		out[f.Field] = f.Message
	}
	return out
}

func workingHost() *Host {
	h := &Host{
		Name:    "api",
		Enabled: true,
		Domains: []string{"api.example.com"},
		Upstreams: []Upstream{
			{Scheme: "http", Address: "10.0.0.1:8080", Weight: 1, Enabled: true},
		},
	}
	h.Normalize()
	return h
}

func TestNormalizeAppliesDefaultsAndCanonicalises(t *testing.T) {
	h := &Host{
		Name:    "  api  ",
		Domains: []string{" API.Example.COM. ", "api.example.com", "", "other.example.com"},
		Upstreams: []Upstream{
			{Scheme: " HTTP ", Address: " 10.0.0.1:8080 ", Weight: 0, MaxConns: -5},
		},
	}
	h.Normalize()

	if h.Name != "api" {
		t.Errorf("name = %q, want it trimmed", h.Name)
	}
	// Case, the trailing dot and the duplicate all collapse to one entry.
	if len(h.Domains) != 2 || h.Domains[0] != "api.example.com" {
		t.Errorf("domains = %v, want two canonical entries", h.Domains)
	}
	if h.Algorithm != RoundRobin {
		t.Errorf("algorithm = %q, want the round robin default", h.Algorithm)
	}

	u := h.Upstreams[0]
	if u.Scheme != "http" || u.Address != "10.0.0.1:8080" {
		t.Errorf("upstream = %+v, want it trimmed and lowercased", u)
	}
	if u.Weight != 1 {
		t.Errorf("weight = %d, want a default of 1", u.Weight)
	}
	if u.MaxConns != 0 {
		t.Errorf("maxConns = %d, want a negative value clamped to 0", u.MaxConns)
	}

	// A timeout longer than the interval would let probes stack up forever.
	if h.HealthCheck.Timeout > h.HealthCheck.Interval {
		t.Errorf("timeout %v exceeds interval %v", h.HealthCheck.Timeout, h.HealthCheck.Interval)
	}
	if h.HealthCheck.Path != "/" {
		t.Errorf("path = %q, want /", h.HealthCheck.Path)
	}

	// Normalize must be safe to run twice.
	before := len(h.Domains)
	h.Normalize()
	if len(h.Domains) != before {
		t.Error("Normalize is not idempotent")
	}
}

func TestNormalizeGivesRelativeHealthPathALeadingSlash(t *testing.T) {
	h := workingHost()
	h.HealthCheck.Path = "healthz"
	h.Normalize()
	if h.HealthCheck.Path != "/healthz" {
		t.Errorf("path = %q, want /healthz", h.HealthCheck.Path)
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	h := &Host{
		Domains:   []string{"has spaces", "-bad.example.com"},
		Algorithm: "magic",
		Upstreams: []Upstream{
			{Scheme: "ftp", Address: "no-port", Weight: 0, Enabled: true},
			{Scheme: "http", Address: "10.0.0.1:99999", Weight: 2000, Enabled: true},
		},
	}
	fields := fieldsOf(t, h.Validate())

	for _, want := range []string{
		"name", "domains[0]", "domains[1]", "algorithm",
		"upstreams[0].scheme", "upstreams[0].address", "upstreams[0].weight",
		"upstreams[1].address", "upstreams[1].weight",
	} {
		if _, ok := fields[want]; !ok {
			t.Errorf("field %q was not reported; got %v", want, fields)
		}
	}
}

func TestValidateRejectsDuplicateUpstreams(t *testing.T) {
	h := workingHost()
	h.Upstreams = append(h.Upstreams, h.Upstreams[0])

	if _, ok := fieldsOf(t, h.Validate())["upstreams[1].address"]; !ok {
		t.Error("a duplicate upstream was accepted")
	}
}

func TestTLSOptionsRequireACertificate(t *testing.T) {
	h := workingHost()
	h.ForceHTTPS = true
	h.HSTSMaxAge = 31536000

	fields := fieldsOf(t, h.Validate())
	if _, ok := fields["forceHttps"]; !ok {
		t.Error("forcing HTTPS without a certificate was accepted")
	}
	if _, ok := fields["hstsMaxAge"]; !ok {
		t.Error("HSTS without a certificate was accepted")
	}

	certID := int64(1)
	h.CertificateID = &certID
	if err := h.Validate(); err != nil {
		t.Errorf("valid host rejected once a certificate is set: %v", err)
	}
}

func TestDomainValidation(t *testing.T) {
	cases := []struct {
		domain string
		valid  bool
		why    string
	}{
		{"example.com", true, ""},
		{"a.b.c.example.com", true, ""},
		{"xn--80ak6aa92e.com", true, "punycode is already ascii"},
		{"*.example.com", true, "single-label wildcard"},
		{"*.com", false, "a wildcard must cover a registrable domain"},
		{"api.*.example.com", false, "wildcard must be leftmost"},
		{"-lead.example.com", false, "label starts with a hyphen"},
		{"trail-.example.com", false, "label ends with a hyphen"},
		{"has space.com", false, "space is not a legal character"},
		{"", false, "empty"},
		{strings.Repeat("a", 64) + ".com", false, "label over 63 characters"},
	}

	for _, tc := range cases {
		h := workingHost()
		h.Domains = []string{tc.domain}
		err := h.Validate()
		got := err == nil
		if got != tc.valid {
			t.Errorf("domain %q accepted=%v, want %v (%s)", tc.domain, got, tc.valid, tc.why)
		}
	}
}

func TestAddressValidation(t *testing.T) {
	cases := []struct {
		address string
		valid   bool
	}{
		{"10.0.0.1:8080", true},
		{"backend.internal:80", true},
		{"[2001:db8::1]:8080", true},
		{"10.0.0.1", false},   // a port is required
		{"10.0.0.1:0", false}, // port out of range
		{"10.0.0.1:70000", false},
		{"10.0.0.1:http", false}, // named ports are not resolved
		{"", false},
	}

	for _, tc := range cases {
		h := workingHost()
		h.Upstreams[0].Address = tc.address
		got := h.Validate() == nil
		if got != tc.valid {
			t.Errorf("address %q accepted=%v, want %v", tc.address, got, tc.valid)
		}
	}
}

func TestEnabledUpstreamsFiltersDisabled(t *testing.T) {
	h := workingHost()
	h.Upstreams = append(h.Upstreams, Upstream{
		Scheme: "http", Address: "10.0.0.2:8080", Weight: 1, Enabled: false,
	})

	if got := h.EnabledUpstreams(); len(got) != 1 || got[0].Address != "10.0.0.1:8080" {
		t.Errorf("EnabledUpstreams = %+v, want only the enabled one", got)
	}
}

func TestUpstreamURL(t *testing.T) {
	u := Upstream{Scheme: "https", Address: "10.0.0.1:8443"}
	if got := u.URL().String(); got != "https://10.0.0.1:8443" {
		t.Errorf("URL = %q", got)
	}
}
