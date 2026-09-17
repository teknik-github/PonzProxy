package proxy

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// gzipWriters are pooled because a gzip writer allocates a 32 KiB window, and
// allocating one per response would make compression cost more in garbage than
// it saves in bandwidth.
//
// One pool per level: a writer Reset for a different level would keep the old
// one, which is a silent wrong answer rather than an error.
var gzipWriters sync.Map // int -> *sync.Pool

func gzipWriterFor(level int) *gzip.Writer {
	pool, _ := gzipWriters.LoadOrStore(level, &sync.Pool{New: func() any {
		w, err := gzip.NewWriterLevel(nil, level)
		if err != nil {
			// Only possible for a level outside 1..9, which validation
			// refuses; falling back keeps a bad value from panicking a
			// request.
			w = gzip.NewWriter(nil)
		}
		return w
	}})
	return pool.(*sync.Pool).Get().(*gzip.Writer)
}

func putGzipWriter(level int, w *gzip.Writer) {
	if pool, ok := gzipWriters.Load(level); ok {
		pool.(*sync.Pool).Put(w)
	}
}

// compressWriter compresses a response on its way to the visitor, if it turns
// out to be worth compressing.
//
// The decision cannot be made when the writer is created, because nothing is
// known then: the content type and the length arrive with the backend's
// headers, and for a chunked response the length never arrives at all. So the
// writer holds the first bytes back until it has enough to decide, then either
// compresses everything from there or writes the held bytes through untouched.
type compressWriter struct {
	http.ResponseWriter
	cfg *domain.Compression

	// decided is set once the writer has committed to compressing or not.
	decided     bool
	compressing bool
	status      int
	wroteHeader bool

	// buf holds the start of the body while the writer is undecided. It is
	// bounded by the configured threshold, so a response that never reaches
	// it is never held in memory beyond that.
	buf []byte

	gz *gzip.Writer
}

// newCompressWriter wraps w when this host compresses and this client accepts
// it. It returns w unchanged otherwise, so a host with compression off pays a
// single comparison.
func newCompressWriter(w http.ResponseWriter, r *http.Request, cfg *domain.Compression) http.ResponseWriter {
	if cfg == nil || !cfg.Enabled {
		return w
	}
	if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		return w
	}
	// A HEAD has no body to compress, and a WebSocket upgrade must reach
	// the hijacker with nothing in between.
	if r.Method == http.MethodHead || isWebSocketUpgrade(r) {
		return w
	}
	return &compressWriter{ResponseWriter: w, cfg: cfg}
}

// acceptsGzip reports whether the client offered gzip and did not refuse it.
//
// "gzip;q=0" is a refusal, and treating it as an offer hands a body the client
// has said it cannot read.
func acceptsGzip(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		token, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		token = strings.ToLower(strings.TrimSpace(token))
		if token != "gzip" && token != "*" {
			continue
		}
		if q, ok := qualityZero(params); ok && q {
			continue
		}
		return true
	}
	return false
}

// qualityZero reports whether the parameters set q=0.
func qualityZero(params string) (zero bool, found bool) {
	for _, p := range strings.Split(params, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		switch strings.TrimSpace(value) {
		case "0", "0.", "0.0", "0.00", "0.000":
			return true, true
		}
		return false, true
	}
	return false, false
}

func (c *compressWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.status = status
	c.wroteHeader = true

	// A response with no body has nothing to compress, and 304 in
	// particular must keep the headers of the response it stands in for.
	if status == http.StatusNoContent || status == http.StatusNotModified ||
		status < http.StatusOK {
		c.decide(false)
		c.ResponseWriter.WriteHeader(status)
		return
	}

	h := c.Header()

	// Already encoded by the backend. Compressing it again spends CPU to
	// make it slightly larger, and produces a Content-Encoding the client
	// has to unwrap twice.
	if h.Get("Content-Encoding") != "" {
		c.decide(false)
		c.ResponseWriter.WriteHeader(status)
		return
	}
	if !c.cfg.Wants(h.Get("Content-Type")) {
		c.decide(false)
		c.ResponseWriter.WriteHeader(status)
		return
	}

	// Any cache between here and the visitor has to know that the body
	// depends on what the client asked for, or it will serve a compressed
	// body to a client that cannot read one.
	addVary(h, "Accept-Encoding")

	// A declared length below the threshold settles it without buffering.
	if n, ok := declaredLength(h); ok && n < int64(c.cfg.MinBytes) {
		c.decide(false)
		c.ResponseWriter.WriteHeader(status)
		return
	}

	// Otherwise the decision waits for the body. The header is not written
	// yet: compressing changes Content-Length, and that cannot be retracted
	// once the status line has gone out.
}

func (c *compressWriter) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	if c.decided {
		return c.write(p)
	}

	c.buf = append(c.buf, p...)
	if len(c.buf) < c.cfg.MinBytes {
		// Still too small to be worth it, and still small enough to hold.
		return len(p), nil
	}

	c.start(true)
	held := c.buf
	c.buf = nil
	if _, err := c.gz.Write(held); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush is what a streaming handler uses to push bytes out now. Holding them
// back to see whether they add up to the threshold would turn a live stream
// into a stalled one, so a flush that has bytes behind it settles the decision.
//
// A flush with nothing buffered is ignored, and that is the important half.
// httputil.ReverseProxy flushes once immediately after the headers for any
// response whose length it does not know — which is most dynamic content.
// Treating that as "the stream has nothing more for now" settled every chunked
// response as uncompressed before a single byte had arrived, so compression
// only ever worked for responses that declared a Content-Length. Nothing is
// written downstream either: sending the headers now would commit them, and
// compressing changes them.
func (c *compressWriter) Flush() {
	if !c.decided {
		if len(c.buf) == 0 {
			// No data, so nothing to decide from and nothing to push.
			return
		}
		// Whatever has arrived is all there is for now. Below the
		// threshold it goes out as it is.
		c.start(len(c.buf) >= c.cfg.MinBytes)
		held := c.buf
		c.buf = nil
		_, _ = c.write(held)
	}
	if c.compressing && c.gz != nil {
		_ = c.gz.Flush()
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// start commits the writer and sends the headers.
func (c *compressWriter) start(compress bool) {
	if c.decided {
		return
	}
	c.decide(compress)

	if compress {
		h := c.Header()
		h.Set("Content-Encoding", "gzip")
		// The compressed length is not known until the body is finished,
		// and a stale one is worse than none: the client would truncate
		// or hang waiting for bytes that never come.
		h.Del("Content-Length")
		// An entity tag identifies a representation, and this is now a
		// different one. Leaving it would let a cache answer an
		// If-None-Match from an uncompressed copy.
		if tag := h.Get("ETag"); tag != "" && !strings.HasPrefix(tag, "W/") {
			h.Set("ETag", "W/"+tag)
		}
		c.gz = gzipWriterFor(c.cfg.Level)
		c.gz.Reset(c.ResponseWriter)
	}
	c.ResponseWriter.WriteHeader(c.statusOrOK())
}

func (c *compressWriter) decide(compress bool) {
	c.decided = true
	c.compressing = compress
}

func (c *compressWriter) write(p []byte) (int, error) {
	if c.compressing {
		return c.gz.Write(p)
	}
	return c.ResponseWriter.Write(p)
}

func (c *compressWriter) statusOrOK() int {
	if c.status == 0 {
		return http.StatusOK
	}
	return c.status
}

// Close finishes the response. It must run for every wrapped request: a gzip
// stream that is never closed is missing its trailer, and the client sees a
// truncated body rather than an error.
func (c *compressWriter) Close() {
	if !c.decided {
		// The whole body fitted under the threshold. It goes out as it is.
		c.start(false)
		if len(c.buf) > 0 {
			_, _ = c.ResponseWriter.Write(c.buf)
			c.buf = nil
		}
		return
	}
	if c.compressing && c.gz != nil {
		_ = c.gz.Close()
		putGzipWriter(c.cfg.Level, c.gz)
		c.gz = nil
	}
}

// Unwrap lets the standard library reach the writer underneath, which is how
// ResponseController finds Flush and Hijack through a wrapper.
func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// declaredLength reads Content-Length, if the backend set one.
func declaredLength(h http.Header) (int64, bool) {
	value := h.Get("Content-Length")
	if value == "" {
		return 0, false
	}
	var n int64
	for _, ch := range []byte(value) {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int64(ch-'0')
	}
	return n, true
}

// addVary appends a field to Vary without repeating one already there.
func addVary(h http.Header, field string) {
	for _, existing := range h.Values("Vary") {
		for _, part := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(part), field) {
				return
			}
			if strings.TrimSpace(part) == "*" {
				return
			}
		}
	}
	h.Add("Vary", field)
}
