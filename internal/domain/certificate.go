package domain

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"strconv"
	"strings"
	"time"
)

// CertSource records where a certificate's key material came from, which
// decides whether ponzproxy may renew it automatically.
type CertSource string

const (
	// CertSourceACME is issued by an ACME CA (Let's Encrypt by default) and
	// is renewed automatically before expiry.
	CertSourceACME CertSource = "acme"
	// CertSourceManual was uploaded by an operator. Never auto-renewed; the
	// UI warns as expiry approaches.
	CertSourceManual CertSource = "manual"
	// CertSourceSelfSigned is generated locally for testing or for internal
	// hostnames no public CA will sign.
	CertSourceSelfSigned CertSource = "self_signed"
)

func (s CertSource) Valid() bool {
	switch s {
	case CertSourceACME, CertSourceManual, CertSourceSelfSigned:
		return true
	}
	return false
}

// ChallengeType is the ACME challenge used to prove domain control.
type ChallengeType string

const (
	// ChallengeHTTP01 answers on :80 at /.well-known/acme-challenge/.
	ChallengeHTTP01 ChallengeType = "http-01"
	// ChallengeTLSALPN01 answers on :443 during the TLS handshake, so no
	// plaintext listener is needed.
	ChallengeTLSALPN01 ChallengeType = "tls-alpn-01"
	// ChallengeDNS01 publishes a TXT record and is the only challenge that
	// can issue wildcard certificates.
	ChallengeDNS01 ChallengeType = "dns-01"
)

func (c ChallengeType) Valid() bool {
	switch c {
	case ChallengeHTTP01, ChallengeTLSALPN01, ChallengeDNS01:
		return true
	}
	return false
}

// Certificate is one stored keypair plus the metadata needed to renew it.
// PrivateKeyPEM is never serialised to API clients.
type Certificate struct {
	ID      int64      `json:"id"`
	Name    string     `json:"name"`
	Domains []string   `json:"domains"`
	Source  CertSource `json:"source"`

	CertificatePEM string `json:"-"`
	PrivateKeyPEM  string `json:"-"`

	// Issuer and the validity window are parsed out of the leaf at save
	// time so the UI can list them without re-parsing PEM on every request.
	Issuer    string    `json:"issuer"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`

	// ACME fields, meaningful only when Source is CertSourceACME.
	Challenge   ChallengeType `json:"challenge,omitempty"`
	DNSProvider string        `json:"dnsProvider,omitempty"`
	// DNSCredentials holds provider-specific secrets as JSON. Withheld from
	// API responses for the same reason as the private key.
	DNSCredentials string `json:"-"`

	// LastError records why the most recent renewal attempt failed, so the
	// UI can surface it instead of silently serving a stale certificate.
	LastError  string     `json:"lastError,omitempty"`
	LastIssued *time.Time `json:"lastIssued,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ExpiresIn is how long the certificate remains valid. It is negative once
// the certificate has expired.
func (c *Certificate) ExpiresIn() time.Duration { return time.Until(c.NotAfter) }

// NeedsRenewal reports whether an ACME certificate has entered its renewal
// window. Let's Encrypt issues for 90 days and recommends renewing with 30
// days left, so renewal starts at one third of the total lifetime remaining.
func (c *Certificate) NeedsRenewal(now time.Time) bool {
	if c.Source != CertSourceACME {
		return false
	}
	if c.CertificatePEM == "" {
		return true
	}
	lifetime := c.NotAfter.Sub(c.NotBefore)
	if lifetime <= 0 {
		return true
	}
	return now.After(c.NotAfter.Add(-lifetime / 3))
}

// TLSCertificate parses the stored PEM into a certificate usable by a TLS
// listener. The result is cached by certmgr, not recomputed per handshake.
func (c *Certificate) TLSCertificate() (tls.Certificate, error) {
	return tls.X509KeyPair([]byte(c.CertificatePEM), []byte(c.PrivateKeyPEM))
}

// Normalize canonicalises operator input the same way Host.Normalize does.
func (c *Certificate) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	domains := make([]string, 0, len(c.Domains))
	seen := make(map[string]struct{}, len(c.Domains))
	for _, d := range c.Domains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(d), ".")))
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		domains = append(domains, d)
	}
	c.Domains = domains
	if c.Source == CertSourceACME && c.Challenge == "" {
		c.Challenge = ChallengeHTTP01
	}
}

// Validate checks the request as a whole, including the combinations that are
// individually fine but cannot work together (wildcards without DNS-01, for
// example).
func (c *Certificate) Validate() error {
	v := &ValidationError{}

	if c.Name == "" {
		v.Add("name", "is required")
	}
	if !c.Source.Valid() {
		v.Add("source", "%q is not a supported source", string(c.Source))
	}
	if len(c.Domains) == 0 {
		v.Add("domains", "at least one domain is required")
	}

	hasWildcard := false
	for i, d := range c.Domains {
		if err := validateDomain(d); err != nil {
			v.Add("domains["+strconv.Itoa(i)+"]", "%s", err.Error())
		}
		if strings.HasPrefix(d, "*.") {
			hasWildcard = true
		}
	}

	switch c.Source {
	case CertSourceACME:
		if !c.Challenge.Valid() {
			v.Add("challenge", "%q is not a supported challenge", string(c.Challenge))
		}
		if hasWildcard && c.Challenge != ChallengeDNS01 {
			v.Add("challenge", "wildcard domains can only be issued with dns-01")
		}
		if c.Challenge == ChallengeDNS01 && c.DNSProvider == "" {
			v.Add("dnsProvider", "is required for the dns-01 challenge")
		}
	case CertSourceManual:
		if err := c.validateKeyPair(); err != nil {
			v.Add("certificate", "%s", err.Error())
		}
	}

	return v.Err()
}

// validateKeyPair confirms an uploaded certificate and key actually belong
// together and cover the declared domains, which is the failure operators hit
// most often when pasting PEM by hand.
func (c *Certificate) validateKeyPair() error {
	if strings.TrimSpace(c.CertificatePEM) == "" {
		return errString("is required")
	}
	if strings.TrimSpace(c.PrivateKeyPEM) == "" {
		return errString("a private key is required")
	}
	pair, err := c.TLSCertificate()
	if err != nil {
		return errString("certificate and private key do not match: " + err.Error())
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errString("certificate could not be parsed: " + err.Error())
	}
	for _, d := range c.Domains {
		if err := leaf.VerifyHostname(d); err != nil {
			return errString("certificate does not cover " + d)
		}
	}
	return nil
}

// ApplyLeafMetadata copies issuer, validity and SANs out of the PEM so the
// stored row stays consistent with the key material it holds.
func (c *Certificate) ApplyLeafMetadata() error {
	block, _ := pem.Decode([]byte(c.CertificatePEM))
	if block == nil {
		return errString("no PEM block found in certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	c.Issuer = leaf.Issuer.CommonName
	if c.Issuer == "" {
		c.Issuer = strings.Join(leaf.Issuer.Organization, ", ")
	}
	c.NotBefore = leaf.NotBefore
	c.NotAfter = leaf.NotAfter
	return nil
}

// MatchesDomain reports whether this certificate covers the given SNI name,
// honouring single-label wildcards.
func (c *Certificate) MatchesDomain(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for _, d := range c.Domains {
		if d == name {
			return true
		}
		// A "*.example.com" wildcard covers exactly one extra label, so
		// "a.example.com" matches but "a.b.example.com" does not.
		if suffix, ok := strings.CutPrefix(d, "*."); ok {
			if idx := strings.IndexByte(name, '.'); idx >= 0 && name[idx+1:] == suffix {
				return true
			}
		}
	}
	return false
}
