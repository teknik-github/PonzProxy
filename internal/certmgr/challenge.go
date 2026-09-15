package certmgr

import (
	"crypto/tls"
	"net/http"
	"strings"
	"sync"
)

// acmeHTTP01Prefix is the fixed path where an HTTP-01 challenge is answered.
const acmeHTTP01Prefix = "/.well-known/acme-challenge/"

// acmeALPNProto is the ALPN protocol a TLS-ALPN-01 validation negotiates.
const acmeALPNProto = "acme-tls/1"

// httpSolver holds the tokens currently being validated over HTTP-01.
//
// Tokens live only for the seconds between publishing a challenge and the CA
// validating it, so a map guarded by a mutex is the right shape: the proxy
// reads it once per request to the challenge path and nowhere else.
type httpSolver struct {
	mu     sync.RWMutex
	tokens map[string]string // request path -> response body
}

func newHTTPSolver() *httpSolver {
	return &httpSolver{tokens: make(map[string]string)}
}

func (s *httpSolver) put(path, response string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[path] = response
}

func (s *httpSolver) remove(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, path)
}

// serve answers a challenge request. It reports false when the request is not
// one, so the caller routes it normally.
func (s *httpSolver) serve(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, acmeHTTP01Prefix) {
		return false
	}

	s.mu.RLock()
	response, ok := s.tokens[r.URL.Path]
	s.mu.RUnlock()
	if !ok {
		// The path is a challenge path but the token is unknown. Claiming
		// it would fail validation anyway, and answering 404 here is also
		// what tells an operator their request reached the wrong server.
		http.NotFound(w, r)
		return true
	}

	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(response))
	return true
}

// tlsALPNSolver holds the temporary certificates that answer TLS-ALPN-01.
//
// This challenge needs no plaintext listener: the CA connects on 443, asks for
// the acme-tls/1 protocol, and the certificate presented in that handshake is
// itself the proof.
type tlsALPNSolver struct {
	mu    sync.RWMutex
	certs map[string]*tls.Certificate // server name -> challenge certificate
}

func newTLSALPNSolver() *tlsALPNSolver {
	return &tlsALPNSolver{certs: make(map[string]*tls.Certificate)}
}

func (s *tlsALPNSolver) put(name string, cert *tls.Certificate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.certs[strings.ToLower(name)] = cert
}

func (s *tlsALPNSolver) remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.certs, strings.ToLower(name))
}

// certificateFor returns the challenge certificate for a handshake, or nil
// when this handshake is not a validation attempt.
func (s *tlsALPNSolver) certificateFor(hello *tls.ClientHelloInfo) *tls.Certificate {
	if !isALPNChallenge(hello) {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.certs[strings.ToLower(hello.ServerName)]
}

// isALPNChallenge reports whether the client offered only the acme-tls/1
// protocol, which is how a validation handshake is distinguished from an
// ordinary one.
func isALPNChallenge(hello *tls.ClientHelloInfo) bool {
	return len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == acmeALPNProto
}
