package certmgr

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/ponzproxy/ponzproxy/internal/certmgr/dnsprovider"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// acmeIssuer talks to the certificate authority. One instance is shared by
// every certificate, since they all use the same account.
type acmeIssuer struct {
	client     *acme.Client
	email      string
	solvers    solverSet
	accountMu  chan struct{} // a one-slot channel used as a context-aware lock
	registered bool
}

// solverSet groups the three ways a challenge can be answered. The manager
// supplies them; the issuer only decides which one a certificate asked for.
type solverSet struct {
	http01    *httpSolver
	tlsALPN01 *tlsALPNSolver
	// dns01 is built per certificate from its stored credentials, so it is
	// a constructor rather than an instance.
	dns01 func(c *domain.Certificate) (dnsprovider.Provider, error)
}

// newACMEIssuer loads or creates the account key under dir. The key is what
// identifies this installation to the CA, so losing it means re-registering
// and losing the issuance history attached to it.
func newACMEIssuer(dir, directoryURL, email string, solvers solverSet) (*acmeIssuer, error) {
	key, err := loadOrCreateAccountKey(filepath.Join(dir, "acme_account.key"))
	if err != nil {
		return nil, err
	}
	lock := make(chan struct{}, 1)
	lock <- struct{}{}

	return &acmeIssuer{
		client: &acme.Client{
			Key:          key,
			DirectoryURL: directoryURL,
			UserAgent:    "ponzproxy",
		},
		email:     email,
		solvers:   solvers,
		accountMu: lock,
	}, nil
}

// Obtain runs a full ACME order and writes the resulting chain and key into c.
func (a *acmeIssuer) Obtain(ctx context.Context, c *domain.Certificate) error {
	if err := a.ensureAccount(ctx); err != nil {
		return err
	}

	order, err := a.client.AuthorizeOrder(ctx, acme.DomainIDs(c.Domains...))
	if err != nil {
		return fmt.Errorf("authorize order: %w", err)
	}

	for _, authzURL := range order.AuthzURLs {
		if err := a.solveAuthorization(ctx, authzURL, c); err != nil {
			return err
		}
	}

	order, err = a.client.WaitOrder(ctx, order.URI)
	if err != nil {
		return fmt.Errorf("wait for order: %w", err)
	}

	// A fresh key per issuance, so a renewal also rotates the key.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate certificate key: %w", err)
	}
	csr, err := certificateRequest(c.Domains, key)
	if err != nil {
		return err
	}

	chain, _, err := a.client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return fmt.Errorf("finalize order: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal certificate key: %w", err)
	}

	var pemChain strings.Builder
	for _, der := range chain {
		if err := pem.Encode(&pemChain, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return err
		}
	}

	c.CertificatePEM = pemChain.String()
	c.PrivateKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	c.Source = domain.CertSourceACME
	c.LastError = ""
	issued := time.Now().UTC()
	c.LastIssued = &issued
	return c.ApplyLeafMetadata()
}

// ensureAccount registers with the CA once per process. An account that
// already exists is not an error — it is the normal case after a restart.
func (a *acmeIssuer) ensureAccount(ctx context.Context) error {
	select {
	case <-a.accountMu:
		defer func() { a.accountMu <- struct{}{} }()
	case <-ctx.Done():
		return ctx.Err()
	}

	if a.registered {
		return nil
	}

	acct := &acme.Account{}
	if a.email != "" {
		acct.Contact = []string{"mailto:" + a.email}
	}
	if _, err := a.client.Register(ctx, acct, acme.AcceptTOS); err != nil &&
		!errors.Is(err, acme.ErrAccountAlreadyExists) {
		return fmt.Errorf("register acme account: %w", err)
	}

	a.registered = true
	return nil
}

// solveAuthorization answers one domain's challenge using the type the
// certificate is configured for.
func (a *acmeIssuer) solveAuthorization(ctx context.Context, authzURL string, c *domain.Certificate) error {
	authz, err := a.client.GetAuthorization(ctx, authzURL)
	if err != nil {
		return fmt.Errorf("get authorization: %w", err)
	}
	if authz.Status == acme.StatusValid {
		// The CA still remembers a recent validation for this domain.
		return nil
	}

	chal := findChallenge(authz.Challenges, string(c.Challenge))
	if chal == nil {
		return fmt.Errorf("the certificate authority does not offer %s for %s",
			c.Challenge, authz.Identifier.Value)
	}

	cleanup, err := a.prepare(ctx, chal, authz, c)
	if err != nil {
		return err
	}
	// Cleanup must run whether validation succeeds or fails, or a stale
	// TXT record or token would be left behind for the next attempt.
	defer cleanup()

	if _, err := a.client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("accept challenge for %s: %w", authz.Identifier.Value, err)
	}
	if _, err := a.client.WaitAuthorization(ctx, authzURL); err != nil {
		return fmt.Errorf("validate %s: %w", authz.Identifier.Value, err)
	}
	return nil
}

// prepare publishes whatever the challenge requires and returns the function
// that withdraws it.
func (a *acmeIssuer) prepare(ctx context.Context, chal *acme.Challenge,
	authz *acme.Authorization, c *domain.Certificate) (func(), error) {

	name := authz.Identifier.Value

	switch domain.ChallengeType(chal.Type) {
	case domain.ChallengeHTTP01:
		response, err := a.client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return nil, err
		}
		path := a.client.HTTP01ChallengePath(chal.Token)
		a.solvers.http01.put(path, response)
		return func() { a.solvers.http01.remove(path) }, nil

	case domain.ChallengeTLSALPN01:
		cert, err := a.client.TLSALPN01ChallengeCert(chal.Token, name)
		if err != nil {
			return nil, err
		}
		a.solvers.tlsALPN01.put(name, &cert)
		return func() { a.solvers.tlsALPN01.remove(name) }, nil

	case domain.ChallengeDNS01:
		provider, err := a.solvers.dns01(c)
		if err != nil {
			return nil, err
		}
		value, err := a.client.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return nil, err
		}
		record := "_acme-challenge." + name
		if err := provider.Present(ctx, record, value); err != nil {
			return nil, fmt.Errorf("publish %s: %w", record, err)
		}
		cleanup := func() {
			// The order's context may already be cancelled, so cleanup
			// gets its own deadline rather than being skipped.
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_ = provider.CleanUp(cctx, record, value)
		}
		if err := waitForTXT(ctx, record, value); err != nil {
			cleanup()
			return nil, err
		}
		return cleanup, nil

	default:
		return nil, fmt.Errorf("unsupported challenge type %q", chal.Type)
	}
}

// waitForTXT blocks until the record is visible in DNS.
//
// Accepting the challenge before propagation is the single most common cause
// of a failed dns-01 issuance, and a failed validation costs a rate-limited
// retry, so it is worth waiting here.
func waitForTXT(ctx context.Context, record, value string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	// A resolver that skips the local cache, since a negative answer for
	// this name may well already be cached from an earlier attempt.
	resolver := &net.Resolver{PreferGo: true}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		values, err := resolver.LookupTXT(ctx, record)
		if err == nil {
			for _, v := range values {
				if v == value {
					return nil
				}
			}
			lastErr = fmt.Errorf("record present but holds %v", values)
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s to propagate: %w", record, lastErr)
		case <-ticker.C:
		}
	}
}

func findChallenge(challenges []*acme.Challenge, want string) *acme.Challenge {
	for _, c := range challenges {
		if c.Type == want {
			return c
		}
	}
	return nil
}

// certificateRequest builds the CSR. The first domain becomes the common name
// and every domain is listed as a SAN, which is what CAs require.
func certificateRequest(domains []string, key *ecdsa.PrivateKey) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: domains[0]},
		DNSNames: domains,
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate request: %w", err)
	}
	return csr, nil
}

// loadOrCreateAccountKey persists the ACME account key with 0600 permissions.
func loadOrCreateAccountKey(path string) (*ecdsa.PrivateKey, error) {
	if raw, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, fmt.Errorf("acme account key at %s is not valid PEM", path)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse acme account key: %w", err)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read acme account key: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate acme account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("persist acme account key: %w", err)
	}
	return key, nil
}
