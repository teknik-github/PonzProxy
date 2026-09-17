package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

var nextCompressHostID atomic.Int64

func compressionOn() domain.Compression {
	c := domain.DefaultCompression()
	c.Enabled = true
	c.MinBytes = 100
	c.Normalize()
	return c
}

// compressHost installs a host whose backend serves whatever handler is given.
func compressHost(t *testing.T, e *Engine, cfg domain.Compression, h http.HandlerFunc) *route {
	t.Helper()
	backend := httptest.NewServer(h)
	t.Cleanup(backend.Close)

	id := 3000 + nextCompressHostID.Add(1)
	name := "gz" + strconv.FormatInt(id, 10) + ".example.com"

	host := domain.Host{
		ID: id, Name: name, Enabled: true,
		Domains:     []string{name},
		Algorithm:   domain.RoundRobin,
		HealthCheck: domain.HealthCheck{Enabled: false},
		Compression: cfg,
		Upstreams: []domain.Upstream{{
			ID: id, HostID: id, Scheme: "http",
			Address: strings.TrimPrefix(backend.URL, "http://"), Weight: 1, Enabled: true,
		}},
	}
	host.Normalize()
	e.Reload(Config{Hosts: []domain.Host{host}})

	rt := e.table.Load().lookup(name)
	if rt == nil {
		t.Fatal("route was not installed")
	}
	return rt
}

// fetchWith makes one request with the given Accept-Encoding.
func fetchWith(t *testing.T, e *Engine, rt *route, accept string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://"+rt.host.Domains[0]+"/", nil)
	r.RemoteAddr = "203.0.113.1:54000"
	if accept != "" {
		r.Header.Set("Accept-Encoding", accept)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

// serveText answers with a body of the given size and content type.
func serveText(contentType string, size int) http.HandlerFunc {
	body := strings.Repeat("the quick brown fox jumps over the lazy dog. ", size/44+1)[:size]
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}
}

func unzip(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("the body is not valid gzip: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("the gzip stream is truncated: %v", err)
	}
	return string(out)
}

func TestATextResponseIsCompressedAndArrivesIntact(t *testing.T) {
	e := testEngine(t)
	rt := compressHost(t, e, compressionOn(), serveText("text/html; charset=utf-8", 4000))

	rec := fetchWith(t, e, rt, "gzip, deflate, br")

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	// A stale length would make the client truncate or hang.
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want it removed", got)
	}
	// Without this any cache in between serves a gzip body to a client that
	// cannot read one.
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	// The whole point: it has to come back out the same.
	if body := unzip(t, rec); len(body) != 4000 {
		t.Errorf("decompressed to %d bytes, want 4000", len(body))
	}
	if rec.Body.Len() >= 4000 {
		t.Errorf("compressed size %d is not smaller than the original", rec.Body.Len())
	}
}

func TestAClientThatDidNotAskGetsItUncompressed(t *testing.T) {
	e := testEngine(t)
	rt := compressHost(t, e, compressionOn(), serveText("text/html", 4000))

	for _, accept := range []string{"", "deflate", "br", "gzip;q=0", "gzip; q=0.0"} {
		rec := fetchWith(t, e, rt, accept)
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("Accept-Encoding %q got Content-Encoding %q", accept, got)
		}
		if rec.Body.Len() != 4000 {
			t.Errorf("Accept-Encoding %q got %d bytes, want the plain 4000", accept, rec.Body.Len())
		}
	}
}

func TestASmallResponseIsLeftAlone(t *testing.T) {
	e := testEngine(t)
	// 24 bytes gzips to 44. Compressing below the threshold makes responses
	// bigger, which is the opposite of the point.
	rt := compressHost(t, e, compressionOn(), serveText("text/html", 24))

	rec := fetchWith(t, e, rt, "gzip")
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("a 24 byte response was compressed (%q)", got)
	}
	if rec.Body.Len() != 24 {
		t.Errorf("body is %d bytes, want the original 24", rec.Body.Len())
	}
}

func TestAlreadyCompressedContentIsNotTouched(t *testing.T) {
	e := testEngine(t)

	// An image: on no list, and re-compressing it would spend CPU to make it
	// very slightly larger.
	rt := compressHost(t, e, compressionOn(), serveText("image/jpeg", 4000))
	if got := fetchWith(t, e, rt, "gzip").Header().Get("Content-Encoding"); got != "" {
		t.Errorf("a JPEG was compressed (%q)", got)
	}

	// A backend that already compressed. Doing it again hands the client a
	// body it has to unwrap twice.
	rt2 := compressHost(t, e, compressionOn(), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, strings.Repeat("x", 4000))
		_ = zw.Close()
	})
	rec := fetchWith(t, e, rt2, "gzip")
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want the backend's own gzip untouched", got)
	}
	if body := unzip(t, rec); len(body) != 4000 {
		t.Errorf("one round of gunzip gave %d bytes; it was compressed twice", len(body))
	}
}

func TestCompressionOffChangesNothing(t *testing.T) {
	e := testEngine(t)
	cfg := domain.DefaultCompression() // enabled is false
	cfg.Normalize()
	rt := compressHost(t, e, cfg, serveText("text/html", 4000))

	rec := fetchWith(t, e, rt, "gzip")
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("a host with compression off returned %q", got)
	}
	if rec.Body.Len() != 4000 {
		t.Errorf("body is %d bytes, want 4000", rec.Body.Len())
	}
}

func TestAChunkedBodyIsCompressedOnceItIsBigEnough(t *testing.T) {
	e := testEngine(t)
	// No Content-Length, so the decision can only be made from the bytes.
	rt := compressHost(t, e, compressionOn(), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		for range 40 {
			_, _ = io.WriteString(w, `{"item":"0123456789012345678901234567890123456789"}`)
		}
	})

	rec := fetchWith(t, e, rt, "gzip")
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if body := unzip(t, rec); len(body) != 40*51 {
		t.Errorf("decompressed to %d bytes, want %d", len(body), 40*51)
	}
}

func TestAShortChunkedBodyStillArrives(t *testing.T) {
	e := testEngine(t)
	// Under the threshold and with no declared length, so the writer holds
	// it to the end and must remember to let it out.
	rt := compressHost(t, e, compressionOn(), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "short")
	})

	rec := fetchWith(t, e, rt, "gzip")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("a five byte body was compressed")
	}
	if got := rec.Body.String(); got != "short" {
		t.Errorf("body = %q, want %q — a held body was dropped", got, "short")
	}
}

func TestAnEmptyBodyIsNotBroken(t *testing.T) {
	e := testEngine(t)
	rt := compressHost(t, e, compressionOn(), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNoContent)
	})

	rec := fetchWith(t, e, rt, "gzip")
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 came back with %d bytes of body", rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("a 204 was given Content-Encoding %q", got)
	}
}

func TestAStrongETagIsWeakenedWhenTheBodyChanges(t *testing.T) {
	e := testEngine(t)
	rt := compressHost(t, e, compressionOn(), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("a{color:red}", 400))
	})

	rec := fetchWith(t, e, rt, "gzip")
	// The tag identifies a representation, and this is a different one. A
	// strong tag here would let a cache answer If-None-Match from the
	// uncompressed copy.
	if got := rec.Header().Get("ETag"); got != `W/"v1"` {
		t.Errorf("ETag = %q, want it weakened to W/\"v1\"", got)
	}
}

func TestVaryIsNotRepeated(t *testing.T) {
	e := testEngine(t)
	rt := compressHost(t, e, compressionOn(), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Vary", "Accept-Encoding, Accept-Language")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("x", 4000))
	})

	rec := fetchWith(t, e, rt, "gzip")
	if got := rec.Header().Values("Vary"); len(got) != 1 {
		t.Errorf("Vary = %v, want the backend's own kept as one value", got)
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := map[string]bool{
		"gzip":                     true,
		"gzip, deflate, br":        true,
		"deflate, gzip;q=0.9":      true,
		"*":                        true,
		"":                         false,
		"deflate":                  false,
		"br":                       false,
		"gzip;q=0":                 false,
		"gzip; q=0.0":              false,
		"identity;q=1, gzip;q=0":   false,
		"identity, gzip ; q=0.001": true,
	}
	for header, want := range cases {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

// TestAHeaderFlushDoesNotSettleTheDecision pins the bug that made compression
// work only for responses declaring a Content-Length.
//
// httputil.ReverseProxy flushes once immediately after the headers for any
// response whose length it does not know. An earlier version treated that as
// "the stream has nothing more for now" and committed to sending the body
// uncompressed before a single byte had arrived — so every chunked response,
// which is most dynamic content, went out whole.
func TestAHeaderFlushDoesNotSettleTheDecision(t *testing.T) {
	e := testEngine(t)
	// No Content-Length, and a handler that flushes before writing anything,
	// exactly as the proxy does between the headers and the body.
	rt := compressHost(t, e, compressionOn(), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, strings.Repeat("a{color:red}", 400))
	})

	rec := fetchWith(t, e, rt, "gzip")
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip — a header flush settled it early", got)
	}
	if body := unzip(t, rec); len(body) != 4800 {
		t.Errorf("decompressed to %d bytes, want 4800", len(body))
	}
}

// TestAStreamIsNotHeldBackOnceItHasWritten covers the other half: a flush that
// does have bytes behind it is a real streaming push, and holding those to see
// whether they add up to the threshold would stall a live stream.
func TestAStreamIsNotHeldBackOnceItHasWritten(t *testing.T) {
	e := testEngine(t)
	rt := compressHost(t, e, compressionOn(), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "tick")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	rec := fetchWith(t, e, rt, "gzip")
	// Four bytes is far under the threshold, so it goes out as it is —
	// promptly, rather than being held for a batch that never comes.
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want a four byte stream left alone", got)
	}
	if got := rec.Body.String(); got != "tick" {
		t.Errorf("body = %q, want %q", got, "tick")
	}
}
