package domain

import "strings"

// Compression compresses responses on their way to the visitor.
//
// It exists because most backends do not. A Go or Node service handing back
// JSON usually sends it uncompressed and leaves the question to whatever sits
// in front — and until now nothing in ponzproxy answered it, so the bytes went
// out whole. On this project's own console bundle that is 1019 kB against 291
// kB, which is the same page and 71% less of the transfer an operator is now
// being billed for and budgeting against.
type Compression struct {
	Enabled bool `json:"enabled"`
	// MinBytes is the smallest response worth compressing. Below it the
	// gzip header and trailer cost more than the compression saves: a 24
	// byte reply comes back at 44 bytes.
	MinBytes int `json:"minBytes"`
	// Types are the content types to compress. Anything already
	// compressed — images, video, archives — is left alone, because
	// re-compressing it spends CPU to make it very slightly larger.
	Types []string `json:"types"`
	// Level is gzip's effort, 1 to 9. The default is a deliberate middle:
	// past about 6 the extra CPU buys single-digit percentages.
	Level int `json:"level"`
}

// Compression bounds.
const (
	MinCompressionBytes = 64
	MaxCompressionBytes = 10 << 20
	MaxCompressionTypes = 32
)

// DefaultCompressionTypes is what a host gets when compression is switched on.
//
// Every entry is a format that is text underneath. text/event-stream is
// deliberately absent: compressing a stream means buffering it, and a server
// sent event that arrives in a batch minutes later is not an event.
func DefaultCompressionTypes() []string {
	return []string{
		"text/html", "text/css", "text/plain", "text/xml",
		"text/javascript", "application/javascript", "application/x-javascript",
		"application/json", "application/xml", "application/rss+xml",
		"image/svg+xml", "application/wasm",
	}
}

// DefaultCompression is applied to new hosts. Off, like every other setting
// that changes what visitors receive: a proxy that silently starts rewriting
// bodies is one whose output an operator can no longer predict.
func DefaultCompression() Compression {
	return Compression{
		Enabled:  false,
		MinBytes: 1024,
		Types:    DefaultCompressionTypes(),
		Level:    5,
	}
}

// Wants reports whether a content type is on the list. The type is matched
// without its parameters, so "text/html; charset=utf-8" matches "text/html".
func (c *Compression) Wants(contentType string) bool {
	media := contentType
	if i := strings.IndexByte(media, ';'); i >= 0 {
		media = media[:i]
	}
	media = strings.ToLower(strings.TrimSpace(media))
	if media == "" {
		return false
	}
	for _, t := range c.Types {
		if t == media {
			return true
		}
	}
	return false
}

func (c *Compression) Normalize() {
	if c.MinBytes == 0 {
		c.MinBytes = 1024
	}
	if c.Level == 0 {
		c.Level = 5
	}

	seen := make(map[string]struct{}, len(c.Types))
	types := make([]string, 0, len(c.Types))
	for _, t := range c.Types {
		t = strings.ToLower(strings.TrimSpace(t))
		if i := strings.IndexByte(t, ';'); i >= 0 {
			t = strings.TrimSpace(t[:i])
		}
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		types = append(types, t)
	}
	c.Types = types
}

func (c *Compression) Validate() error {
	v := &ValidationError{}

	if c.MinBytes < MinCompressionBytes || c.MinBytes > MaxCompressionBytes {
		v.Add("compression.minBytes", "must be between %d and %d",
			MinCompressionBytes, MaxCompressionBytes)
	}
	if c.Level < 1 || c.Level > 9 {
		v.Add("compression.level", "must be between 1 and 9")
	}
	if len(c.Types) > MaxCompressionTypes {
		v.Add("compression.types", "must be at most %d", MaxCompressionTypes)
	}
	for _, t := range c.Types {
		if !strings.Contains(t, "/") || strings.ContainsAny(t, " \t") {
			v.Add("compression.types", "%q is not a content type", t)
		}
	}
	// A list with nothing on it compresses nothing, which looks switched on
	// and does nothing — worse than being off, because it invites an
	// operator to go looking for the fault somewhere else.
	if c.Enabled && len(c.Types) == 0 {
		v.Add("compression.types", "list at least one content type, or switch compression off")
	}
	return v.Err()
}
