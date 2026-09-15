// Package certmgr owns TLS material: it resolves certificates for incoming
// handshakes, imports what an operator uploads, generates self-signed pairs
// and obtains real ones from an ACME certificate authority.
package certmgr

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// selfSignedLifetime is how long a generated certificate stays valid. One year
// is long enough to be useful for internal hostnames and short enough that a
// forgotten certificate eventually announces itself.
const selfSignedLifetime = 365 * 24 * time.Hour

// GenerateSelfSigned produces a certificate and key for the given domains and
// writes them into c. It is used for internal hostnames and for testing, where
// no public CA will issue.
func GenerateSelfSigned(c *domain.Certificate) error {
	if len(c.Domains) == 0 {
		return fmt.Errorf("at least one domain is required")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   c.Domains[0],
			Organization: []string{"ponzproxy self-signed"},
		},
		// Backdated by an hour so a small clock skew on the client does
		// not make a freshly issued certificate look not-yet-valid.
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, d := range c.Domains {
		if ip := net.ParseIP(d); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		// A wildcard SAN is stored verbatim; Go's VerifyHostname applies
		// the single-label rule when matching it.
		tmpl.DNSNames = append(tmpl.DNSNames, d)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}

	c.CertificatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	c.PrivateKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	c.Source = domain.CertSourceSelfSigned
	c.LastError = ""
	issued := now.UTC()
	c.LastIssued = &issued

	return c.ApplyLeafMetadata()
}

// ImportManual validates an operator-supplied pair and records its metadata.
// The certificate is rejected rather than stored when it does not match its
// key or does not cover the domains it is registered for, because both
// failures would otherwise only surface as a handshake error in production.
func ImportManual(c *domain.Certificate) error {
	c.Source = domain.CertSourceManual
	c.Normalize()

	c.CertificatePEM = normalizePEM(c.CertificatePEM)
	c.PrivateKeyPEM = normalizePEM(c.PrivateKeyPEM)

	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.ApplyLeafMetadata(); err != nil {
		return err
	}
	c.LastError = ""
	issued := time.Now().UTC()
	c.LastIssued = &issued
	return nil
}

// normalizePEM repairs the most common damage from pasting PEM into a form:
// Windows line endings and missing surrounding whitespace.
func normalizePEM(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return s + "\n"
}
