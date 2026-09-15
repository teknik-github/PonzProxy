package proxy

import "net/http"

// recorder wraps the client's ResponseWriter to learn the status code and the
// number of bytes sent, which the metrics collector and the access log both
// need and neither can otherwise see.
//
// It deliberately implements nothing beyond the ResponseWriter interface plus
// Unwrap. http.ResponseController reaches Flush, Hijack and the deadline
// setters through Unwrap, which is what keeps WebSocket upgrades and streaming
// responses working without this type having to forward each method — and
// without it silently hiding a capability the real writer has.
type recorder struct {
	http.ResponseWriter

	status  int
	written int64
	// headerSent records whether the status line has gone out, since a
	// status of 0 is indistinguishable from an unset one.
	headerSent bool
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) WriteHeader(status int) {
	if r.headerSent {
		// net/http already logs the duplicate; swallowing it here keeps
		// the recorded status as the one the client actually received.
		return
	}
	r.status = status
	r.headerSent = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.headerSent {
		// Writing without an explicit status implies 200, and the metrics
		// need to know that.
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// dirty reports whether anything has reached the client yet. Once it has, the
// request can no longer be retried against another backend and no error page
// can replace what was already sent.
func (r *recorder) dirty() bool { return r.headerSent || r.written > 0 }
