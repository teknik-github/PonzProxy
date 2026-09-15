package certmgr

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestGenerateSelfSignedProducesAUsablePair(t *testing.T) {
	c := &domain.Certificate{Name: "internal", Domains: []string{"box.internal", "10.0.0.5"}}
	if err := GenerateSelfSigned(c); err != nil {
		t.Fatalf("generate: %v", err)
	}

	if c.Source != domain.CertSourceSelfSigned {
		t.Errorf("source = %q, want self_signed", c.Source)
	}
	pair, err := c.TLSCertificate()
	if err != nil {
		t.Fatalf("the generated pair does not load: %v", err)
	}
	if pair.Leaf == nil {
		if _, err := c.TLSCertificate(); err != nil {
			t.Fatal(err)
		}
	}
	if c.NotAfter.Before(time.Now().Add(300 * 24 * time.Hour)) {
		t.Errorf("expires at %v, want roughly a year out", c.NotAfter)
	}
	// Backdated validity absorbs client clock skew.
	if !c.NotBefore.Before(time.Now()) {
		t.Error("certificate is not yet valid at the moment it was created")
	}
	// A self-signed certificate is never auto-renewed.
	if c.NeedsRenewal(time.Now().Add(400 * 24 * time.Hour)) {
		t.Error("a self-signed certificate should never be queued for renewal")
	}
}

func TestSelfSignedCoversWildcards(t *testing.T) {
	c := &domain.Certificate{Name: "wild", Domains: []string{"*.example.com"}}
	if err := GenerateSelfSigned(c); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !c.MatchesDomain("api.example.com") {
		t.Error("wildcard does not match a single label")
	}
	if c.MatchesDomain("a.b.example.com") {
		t.Error("wildcard matched two labels")
	}
}

func TestImportManualRejectsMismatchedKey(t *testing.T) {
	good := &domain.Certificate{Name: "a", Domains: []string{"a.example.com"}}
	if err := GenerateSelfSigned(good); err != nil {
		t.Fatal(err)
	}
	other := &domain.Certificate{Name: "b", Domains: []string{"b.example.com"}}
	if err := GenerateSelfSigned(other); err != nil {
		t.Fatal(err)
	}

	mixed := &domain.Certificate{
		Name:           "mixed",
		Domains:        []string{"a.example.com"},
		CertificatePEM: good.CertificatePEM,
		PrivateKeyPEM:  other.PrivateKeyPEM, // the wrong key
	}
	if err := ImportManual(mixed); err == nil {
		t.Fatal("a certificate and key that do not match were accepted")
	}
}

func TestImportManualRejectsUncoveredDomain(t *testing.T) {
	src := &domain.Certificate{Name: "a", Domains: []string{"a.example.com"}}
	if err := GenerateSelfSigned(src); err != nil {
		t.Fatal(err)
	}

	wrong := &domain.Certificate{
		Name:           "wrong",
		Domains:        []string{"different.example.com"},
		CertificatePEM: src.CertificatePEM,
		PrivateKeyPEM:  src.PrivateKeyPEM,
	}
	if err := ImportManual(wrong); err == nil {
		t.Fatal("a certificate that does not cover its declared domain was accepted")
	}
}

func TestImportManualToleratesCRLFAndWhitespace(t *testing.T) {
	src := &domain.Certificate{Name: "a", Domains: []string{"a.example.com"}}
	if err := GenerateSelfSigned(src); err != nil {
		t.Fatal(err)
	}

	pasted := &domain.Certificate{
		Name:           "pasted",
		Domains:        []string{" A.Example.com "},
		CertificatePEM: "  \n" + strings.ReplaceAll(src.CertificatePEM, "\n", "\r\n") + "  ",
		PrivateKeyPEM:  strings.ReplaceAll(src.PrivateKeyPEM, "\n", "\r\n"),
	}
	if err := ImportManual(pasted); err != nil {
		t.Fatalf("PEM pasted with CRLF and stray whitespace was rejected: %v", err)
	}
	if pasted.Domains[0] != "a.example.com" {
		t.Errorf("domain = %q, want it normalized", pasted.Domains[0])
	}
}

func TestCertIndexPrefersExactOverWildcard(t *testing.T) {
	exact := domain.Certificate{ID: 1, Name: "exact", Domains: []string{"api.example.com"}}
	wild := domain.Certificate{ID: 2, Name: "wild", Domains: []string{"*.example.com"}}
	for _, c := range []*domain.Certificate{&exact, &wild} {
		if err := GenerateSelfSigned(c); err != nil {
			t.Fatal(err)
		}
	}

	idx := newCertIndex([]domain.Certificate{exact, wild}, discard())
	if idx.size() != 2 {
		t.Fatalf("indexed %d certificates, want 2", idx.size())
	}

	apiCert := idx.lookup("api.example.com")
	wildCert := idx.lookup("other.example.com")
	if apiCert == nil || wildCert == nil {
		t.Fatal("lookup returned nothing for a configured name")
	}
	if apiCert == wildCert {
		t.Error("the exact name resolved to the wildcard certificate")
	}
	if idx.lookup("a.b.example.com") != nil {
		t.Error("wildcard matched two labels")
	}
	if idx.lookup("nothing.test") != nil {
		t.Error("an unconfigured name resolved to a certificate")
	}
}

func TestCertIndexSkipsUnparseableCertificates(t *testing.T) {
	good := domain.Certificate{ID: 1, Name: "good", Domains: []string{"good.example.com"}}
	if err := GenerateSelfSigned(&good); err != nil {
		t.Fatal(err)
	}
	broken := domain.Certificate{
		ID: 2, Name: "broken", Domains: []string{"broken.example.com"},
		CertificatePEM: "-----BEGIN CERTIFICATE-----\nnot base64\n-----END CERTIFICATE-----\n",
		PrivateKeyPEM:  "-----BEGIN EC PRIVATE KEY-----\nalso not\n-----END EC PRIVATE KEY-----\n",
	}
	pending := domain.Certificate{ID: 3, Name: "pending", Domains: []string{"pending.example.com"}}

	idx := newCertIndex([]domain.Certificate{good, broken, pending}, discard())

	if idx.size() != 1 {
		t.Errorf("indexed %d certificates, want only the usable one", idx.size())
	}
	if idx.lookup("good.example.com") == nil {
		t.Error("a broken certificate stopped a good one from being served")
	}
	if idx.lookup("broken.example.com") != nil {
		t.Error("an unparseable certificate was served")
	}
}

func TestHTTP01SolverAnswersOnlyKnownTokens(t *testing.T) {
	s := newHTTPSolver()
	s.put(acmeHTTP01Prefix+"tok", "tok.keyauth")

	w := httptest.NewRecorder()
	if !s.serve(w, httptest.NewRequest(http.MethodGet, acmeHTTP01Prefix+"tok", nil)) {
		t.Fatal("a challenge request was not handled")
	}
	if got := w.Body.String(); got != "tok.keyauth" {
		t.Errorf("body = %q, want the key authorization", got)
	}

	w = httptest.NewRecorder()
	if !s.serve(w, httptest.NewRequest(http.MethodGet, acmeHTTP01Prefix+"other", nil)) {
		t.Fatal("an unknown challenge path should still be claimed")
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown token status = %d, want 404", w.Code)
	}

	// Anything outside the challenge prefix must be left to the proxy.
	w = httptest.NewRecorder()
	if s.serve(w, httptest.NewRequest(http.MethodGet, "/index.html", nil)) {
		t.Error("an ordinary request was swallowed by the challenge solver")
	}

	s.remove(acmeHTTP01Prefix + "tok")
	w = httptest.NewRecorder()
	s.serve(w, httptest.NewRequest(http.MethodGet, acmeHTTP01Prefix+"tok", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status after cleanup = %d, want 404", w.Code)
	}
}

func TestALPNChallengeIsDetectedByProtocolList(t *testing.T) {
	s := newTLSALPNSolver()
	cert := &tls.Certificate{}
	s.put("api.example.com", cert)

	challenge := &tls.ClientHelloInfo{
		ServerName:      "api.example.com",
		SupportedProtos: []string{acmeALPNProto},
	}
	if got := s.certificateFor(challenge); got != cert {
		t.Error("a validation handshake did not get the challenge certificate")
	}

	// An ordinary browser handshake must never receive it, even for the
	// same server name.
	ordinary := &tls.ClientHelloInfo{
		ServerName:      "api.example.com",
		SupportedProtos: []string{"h2", "http/1.1"},
	}
	if got := s.certificateFor(ordinary); got != nil {
		t.Error("an ordinary handshake was served the challenge certificate")
	}

	s.remove("api.example.com")
	if got := s.certificateFor(challenge); got != nil {
		t.Error("the challenge certificate survived cleanup")
	}
}
