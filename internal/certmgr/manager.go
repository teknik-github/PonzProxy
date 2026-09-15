package certmgr

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/certmgr/dnsprovider"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// ErrNoCertificate is returned for a handshake that matches nothing. The TLS
// stack turns it into an unrecognised-name alert, which is the correct answer
// for a server name this proxy does not terminate.
var ErrNoCertificate = errors.New("no certificate matches this server name")

// Manager resolves certificates for incoming handshakes and keeps ACME
// certificates renewed.
//
// It holds a parsed copy of every certificate in memory. Parsing PEM per
// handshake would be both slow and a denial-of-service lever, so the index is
// rebuilt on configuration change instead.
type Manager struct {
	repo   domain.CertificateRepository
	logger *slog.Logger

	http01 *httpSolver
	alpn   *tlsALPNSolver
	issuer *acmeIssuer

	// mu guards the resolution index, which is replaced wholesale by
	// Reload rather than mutated in place.
	mu    sync.RWMutex
	index *certIndex

	// issuing serialises orders so a burst of saves cannot open several
	// ACME orders for the same domain at once.
	issuing sync.Mutex
}

// Config carries what the manager needs from process configuration.
type Config struct {
	// Dir is where the ACME account key is kept.
	Dir string
	// DirectoryURL is the ACME endpoint. Point it at a staging endpoint
	// while testing: production issuance is rate limited per domain.
	DirectoryURL string
	// Email receives expiry warnings from the CA. Optional but recommended.
	Email string
}

// New builds a manager and loads the current certificates.
func New(ctx context.Context, repo domain.CertificateRepository, cfg Config, logger *slog.Logger) (*Manager, error) {
	m := &Manager{
		repo:   repo,
		logger: logger.With("component", "certmgr"),
		http01: newHTTPSolver(),
		alpn:   newTLSALPNSolver(),
		index:  newCertIndex(nil, logger),
	}

	issuer, err := newACMEIssuer(cfg.Dir, cfg.DirectoryURL, cfg.Email, solverSet{
		http01:    m.http01,
		tlsALPN01: m.alpn,
		dns01:     dnsProviderFor,
	})
	if err != nil {
		return nil, err
	}
	m.issuer = issuer

	if err := m.Reload(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// Reload rebuilds the resolution index from the store. It is called at boot
// and after any certificate change.
func (m *Manager) Reload(ctx context.Context) error {
	certs, err := m.repo.List(ctx)
	if err != nil {
		return fmt.Errorf("load certificates: %w", err)
	}

	index := newCertIndex(certs, m.logger)

	m.mu.Lock()
	m.index = index
	m.mu.Unlock()

	m.logger.Info("certificates loaded", "count", index.size())
	return nil
}

// CertificateFor implements proxy.CertificateResolver.
func (m *Manager) CertificateFor(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	// A validation handshake is answered first and never falls through to
	// the real certificates: the CA is not asking for one.
	if cert := m.alpn.certificateFor(hello); cert != nil {
		return cert, nil
	}
	if isALPNChallenge(hello) {
		return nil, fmt.Errorf("%w: no tls-alpn-01 challenge is in progress for %q",
			ErrNoCertificate, hello.ServerName)
	}

	m.mu.RLock()
	index := m.index
	m.mu.RUnlock()

	if cert := index.lookup(hello.ServerName); cert != nil {
		return cert, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrNoCertificate, hello.ServerName)
}

// HandleACMEChallenge implements proxy.CertificateResolver.
func (m *Manager) HandleACMEChallenge(w http.ResponseWriter, r *http.Request) bool {
	return m.http01.serve(w, r)
}

// Issue obtains or renews one certificate and saves the result.
//
// Failures are recorded on the certificate rather than only returned, so an
// operator can see why a renewal is not happening without reading logs.
func (m *Manager) Issue(ctx context.Context, id int64) error {
	m.issuing.Lock()
	defer m.issuing.Unlock()

	cert, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if cert.Source != domain.CertSourceACME {
		return fmt.Errorf("certificate %q is not an ACME certificate", cert.Name)
	}

	m.logger.Info("requesting certificate",
		"name", cert.Name, "domains", cert.Domains, "challenge", cert.Challenge)

	issueErr := m.issuer.Obtain(ctx, cert)
	if issueErr != nil {
		cert.LastError = issueErr.Error()
		if err := m.repo.Update(ctx, cert); err != nil {
			m.logger.Error("record issuance failure", "name", cert.Name, "error", err)
		}
		return issueErr
	}

	if err := m.repo.Update(ctx, cert); err != nil {
		return fmt.Errorf("save issued certificate: %w", err)
	}
	m.logger.Info("certificate issued",
		"name", cert.Name, "expires", cert.NotAfter.Format(time.RFC3339))

	return m.Reload(ctx)
}

// Run renews ACME certificates as they enter their renewal window. It blocks
// until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	// Twice a day is ample: the renewal window is a third of the
	// certificate lifetime, which is 30 days for Let's Encrypt.
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()

	m.renewDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.renewDue(ctx)
		}
	}
}

func (m *Manager) renewDue(ctx context.Context) {
	due, err := m.repo.DueForRenewal(ctx, time.Now())
	if err != nil {
		m.logger.Error("check certificates for renewal", "error", err)
		return
	}
	for _, cert := range due {
		if ctx.Err() != nil {
			return
		}
		if err := m.Issue(ctx, cert.ID); err != nil {
			// One failure must not stop the others from being attempted;
			// it is already recorded on the certificate itself.
			m.logger.Error("renew certificate", "name", cert.Name, "error", err)
		}
	}
}

// dnsProviderFor builds the dns-01 solver a certificate is configured with.
func dnsProviderFor(c *domain.Certificate) (dnsprovider.Provider, error) {
	creds := json.RawMessage(c.DNSCredentials)
	if len(creds) == 0 {
		creds = json.RawMessage("{}")
	}
	return dnsprovider.New(c.DNSProvider, creds)
}

// certIndex resolves a server name to a parsed certificate.
//
// Exact names and wildcards are separated for the same reason the routing
// table separates them: lookup stays a map hit, and an exact match always wins
// over a wildcard covering the same name.
type certIndex struct {
	exact    map[string]*tls.Certificate
	wildcard map[string]*tls.Certificate
	count    int
}

func newCertIndex(certs []domain.Certificate, logger *slog.Logger) *certIndex {
	idx := &certIndex{
		exact:    make(map[string]*tls.Certificate, len(certs)),
		wildcard: make(map[string]*tls.Certificate),
	}

	for i := range certs {
		c := &certs[i]
		if c.CertificatePEM == "" || c.PrivateKeyPEM == "" {
			continue // requested but not yet issued
		}
		parsed, err := c.TLSCertificate()
		if err != nil {
			// One unusable certificate must not stop the others from
			// being served, so this is logged and skipped.
			logger.Error("certificate could not be parsed; it will not be served",
				"name", c.Name, "id", c.ID, "error", err)
			continue
		}
		idx.count++

		for _, d := range c.Domains {
			if parent, ok := strings.CutPrefix(d, "*."); ok {
				idx.wildcard[parent] = &parsed
				continue
			}
			idx.exact[d] = &parsed
		}
	}
	return idx
}

func (i *certIndex) size() int { return i.count }

func (i *certIndex) lookup(serverName string) *tls.Certificate {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(serverName), "."))
	if name == "" {
		return nil
	}
	if cert, ok := i.exact[name]; ok {
		return cert
	}
	if _, parent, found := strings.Cut(name, "."); found {
		if cert, ok := i.wildcard[parent]; ok {
			return cert
		}
	}
	return nil
}
