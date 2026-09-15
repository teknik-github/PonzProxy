package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/certmgr"
	"github.com/ponzproxy/ponzproxy/internal/certmgr/dnsprovider"
	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// certificateView is what the API returns. It never carries key material:
// the private key exists to be used by the TLS listener, not to be read back
// out over HTTP.
type certificateView struct {
	domain.Certificate
	// ExpiresInDays is derived so the UI does not have to do date maths to
	// decide what to highlight.
	ExpiresInDays int  `json:"expiresInDays"`
	Installed     bool `json:"installed"`
	// InUseByHosts reports how many hosts terminate TLS with it, which is
	// also what makes deletion fail.
	InUseByHosts int `json:"inUseByHosts"`
}

func (s *Server) viewOf(r *http.Request, c domain.Certificate) certificateView {
	view := certificateView{
		Certificate:   c,
		Installed:     c.CertificatePEM != "",
		ExpiresInDays: int(c.ExpiresIn().Hours() / 24),
	}
	if n, err := s.opts.Hosts.CountByCertificate(r.Context(), c.ID); err == nil {
		view.InUseByHosts = n
	}
	// A redirect can terminate TLS too, and the database refuses to delete a
	// certificate either is using. Counting only hosts would show it as
	// unused and make that refusal look like a bug.
	if n, err := s.opts.Redirects.CountByCertificate(r.Context(), c.ID); err == nil {
		view.InUseByHosts += n
	}
	return view
}

func (s *Server) handleListCertificates(w http.ResponseWriter, r *http.Request) {
	certs, err := s.opts.Certs.List(r.Context())
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	views := make([]certificateView, 0, len(certs))
	for _, c := range certs {
		views = append(views, s.viewOf(r, c))
	}
	writeJSON(w, s.logger, http.StatusOK, views)
}

// certificatePayload is the write shape. Which fields matter depends on
// Source, which is validated by the domain type.
type certificatePayload struct {
	Name    string            `json:"name"`
	Domains []string          `json:"domains"`
	Source  domain.CertSource `json:"source"`

	// Manual uploads.
	CertificatePEM string `json:"certificatePem,omitempty"`
	PrivateKeyPEM  string `json:"privateKeyPem,omitempty"`

	// ACME.
	Challenge      domain.ChallengeType `json:"challenge,omitempty"`
	DNSProvider    string               `json:"dnsProvider,omitempty"`
	DNSCredentials map[string]string    `json:"dnsCredentials,omitempty"`
}

func (s *Server) handleCreateCertificate(w http.ResponseWriter, r *http.Request) {
	var payload certificatePayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, s.logger, err)
		return
	}

	cert := &domain.Certificate{
		Name:           payload.Name,
		Domains:        payload.Domains,
		Source:         payload.Source,
		CertificatePEM: payload.CertificatePEM,
		PrivateKeyPEM:  payload.PrivateKeyPEM,
		Challenge:      payload.Challenge,
		DNSProvider:    payload.DNSProvider,
	}

	if len(payload.DNSCredentials) > 0 {
		encoded, err := json.Marshal(payload.DNSCredentials)
		if err != nil {
			writeError(w, s.logger, err)
			return
		}
		cert.DNSCredentials = string(encoded)
	}

	// Each source is prepared differently: self-signed material is
	// generated now, manual material is validated now, and ACME material
	// only exists after the order runs.
	switch cert.Source {
	case domain.CertSourceSelfSigned:
		cert.Normalize()
		if err := cert.Validate(); err != nil {
			writeError(w, s.logger, err)
			return
		}
		if err := certmgr.GenerateSelfSigned(cert); err != nil {
			writeError(w, s.logger, err)
			return
		}
	case domain.CertSourceManual:
		if err := certmgr.ImportManual(cert); err != nil {
			writeError(w, s.logger, err)
			return
		}
	case domain.CertSourceACME:
		cert.Normalize()
		if err := cert.Validate(); err != nil {
			writeError(w, s.logger, err)
			return
		}
		if cert.Challenge == domain.ChallengeDNS01 {
			// Fail on a bad provider name or malformed credentials now,
			// rather than during an order that counts against the CA's
			// rate limit.
			if _, err := dnsprovider.New(cert.DNSProvider, json.RawMessage(cert.DNSCredentials)); err != nil {
				v := &domain.ValidationError{}
				v.Add("dnsProvider", "%s", err.Error())
				writeError(w, s.logger, v)
				return
			}
		}
	default:
		v := &domain.ValidationError{}
		v.Add("source", "%q is not a supported source", string(cert.Source))
		writeError(w, s.logger, v)
		return
	}

	if err := s.opts.Certs.Create(r.Context(), cert); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.CertManager.Reload(r.Context()); err != nil {
		s.logger.Error("reload certificates", "error", err)
	}

	s.logger.Info("certificate created",
		"name", cert.Name, "source", cert.Source, "domains", cert.Domains)
	writeJSON(w, s.logger, http.StatusCreated, s.viewOf(r, *cert))
}

// handleIssueCertificate runs an ACME order for an existing certificate.
//
// Issuance involves network round trips to the CA and, for dns-01, waiting for
// propagation, so it runs in the background and the caller is told to watch
// the certificate's state rather than being held on an open request.
func (s *Server) handleIssueCertificate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	cert, err := s.opts.Certs.Get(r.Context(), id)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if cert.Source != domain.CertSourceACME {
		writeError(w, s.logger, errors.Join(errBadRequest,
			errors.New("only ACME certificates can be issued")))
		return
	}

	s.issueInBackground(cert.ID, cert.Name)
	writeJSON(w, s.logger, http.StatusAccepted, map[string]string{
		"status": "issuing",
		"detail": "The order is running. Watch the certificate for its result.",
	})
}

// issueInBackground detaches the order from the request. The context comes
// from the server's lifetime, not the request, so the order is not cancelled
// the moment the operator's browser gets its 202.
func (s *Server) issueInBackground(id int64, name string) {
	go func() {
		ctx, cancel := context.WithTimeout(s.baseCtx, 10*time.Minute)
		defer cancel()

		if err := s.opts.CertManager.Issue(ctx, id); err != nil {
			s.logger.Error("issue certificate", "name", name, "error", err)
			return
		}
		s.logger.Info("certificate ready", "name", name)
	}()
}

func (s *Server) handleDeleteCertificate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.Certs.Delete(r.Context(), id); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.CertManager.Reload(r.Context()); err != nil {
		s.logger.Error("reload certificates", "error", err)
	}

	s.logger.Info("certificate deleted", "id", id)
	writeJSON(w, s.logger, http.StatusNoContent, nil)
}

// handleListDNSProviders tells the UI which dns-01 providers exist and what
// credentials each needs, so the form is built from the server's registry.
func (s *Server) handleListDNSProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.logger, http.StatusOK, dnsprovider.Descriptors())
}
