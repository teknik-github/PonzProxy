package domain

import (
	"net/http"
	"strconv"
	"strings"
)

// HeaderRule sets or removes one header.
//
// Remove is a flag rather than an empty Value because an empty header value is
// legal and occasionally meaningful, so overloading it would make one of the
// two impossible to express.
type HeaderRule struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Remove bool   `json:"remove"`
}

// Headers are the per-host and per-location header rules.
//
// Request rules apply to what the backend receives; Response rules to what the
// visitor receives. They are separate lists because the two have nothing to do
// with each other: adding an internal auth header for a backend and adding a
// Content-Security-Policy for a browser are different jobs that happen to use
// the same mechanism.
type Headers struct {
	Request  []HeaderRule `json:"request"`
	Response []HeaderRule `json:"response"`
}

// MaxHeaderRules bounds one list. Past this it is a rewrite engine, which
// wants a different design from a form.
const MaxHeaderRules = 32

// protectedHeaders are the ones an operator must not set here.
//
// Every one of them describes the connection or the framing of the message
// rather than its content, and setting it by hand does not change what the
// proxy actually does — it only makes the headers disagree with reality.
// Content-Length is the clearest case: writing one does not change the body,
// it just creates a response the client will parse wrongly.
var protectedHeaders = map[string]string{
	"connection":          "the proxy manages the connection",
	"transfer-encoding":   "the proxy decides the framing",
	"content-length":      "setting it does not change the body, it only makes the two disagree",
	"upgrade":             "WebSocket upgrades are a per-host switch",
	"keep-alive":          "the proxy manages the connection",
	"proxy-authenticate":  "hop-by-hop, and meaningless past this proxy",
	"proxy-authorization": "hop-by-hop, and meaningless past this proxy",
	"te":                  "hop-by-hop",
	"trailer":             "hop-by-hop",
	"host":                `use "Send the original Host header upstream" instead`,
}

// Empty reports whether there is nothing to apply, so the request path can
// skip the work entirely.
func (h *Headers) Empty() bool { return len(h.Request) == 0 && len(h.Response) == 0 }

// ApplyRequest edits the headers going to a backend.
func (h *Headers) ApplyRequest(header http.Header) { applyRules(header, h.Request) }

// ApplyResponse edits the headers going back to the visitor.
func (h *Headers) ApplyResponse(header http.Header) { applyRules(header, h.Response) }

func applyRules(header http.Header, rules []HeaderRule) {
	for _, r := range rules {
		if r.Remove {
			header.Del(r.Name)
			continue
		}
		// Set rather than Add: an operator writing a rule means "this
		// header should be this", and appending to whatever the backend
		// already sent produces two values and no way to predict which
		// one anything downstream reads.
		header.Set(r.Name, r.Value)
	}
}

// Normalize canonicalises names and drops blank rows, which a form produces
// every time someone adds a rule and changes their mind.
func (h *Headers) Normalize() {
	h.Request = normalizeRules(h.Request)
	h.Response = normalizeRules(h.Response)
}

func normalizeRules(rules []HeaderRule) []HeaderRule {
	out := make([]HeaderRule, 0, len(rules))
	for _, r := range rules {
		r.Name = http.CanonicalHeaderKey(strings.TrimSpace(r.Name))
		r.Value = strings.TrimSpace(r.Value)
		if r.Name == "" {
			continue
		}
		if r.Remove {
			r.Value = ""
		}
		out = append(out, r)
	}
	return out
}

// Validate reports every problem at once, under the given field prefix so the
// same rules can be checked for a host and for each of its locations.
func (h *Headers) Validate(v *ValidationError, prefix string) {
	validateRules(v, prefix+".request", h.Request)
	validateRules(v, prefix+".response", h.Response)
}

func validateRules(v *ValidationError, prefix string, rules []HeaderRule) {
	if len(rules) > MaxHeaderRules {
		v.Add(prefix, "must be at most %d rules", MaxHeaderRules)
	}
	seen := make(map[string]struct{}, len(rules))
	for i, r := range rules {
		field := prefix + "[" + strconv.Itoa(i) + "]"

		if r.Name == "" {
			v.Add(field+".name", "give the header a name")
			continue
		}
		if !validHeaderName(r.Name) {
			v.Add(field+".name", "%q is not a valid header name", r.Name)
		}
		if why, protected := protectedHeaders[strings.ToLower(r.Name)]; protected {
			v.Add(field+".name", "%s cannot be set here: %s", r.Name, why)
		}
		if !validHeaderValue(r.Value) {
			// A newline in a value is header injection: it ends the
			// header and starts another one the operator did not write.
			v.Add(field+".value", "must not contain control characters or line breaks")
		}
		if _, dup := seen[r.Name]; dup {
			v.Add(field+".name", "duplicate rule for %s", r.Name)
		}
		seen[r.Name] = struct{}{}
	}
}

// validHeaderName reports whether a name is a valid HTTP token.
func validHeaderName(name string) bool {
	for _, c := range []byte(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return name != ""
}

func validHeaderValue(value string) bool {
	for _, c := range []byte(value) {
		if c < 0x20 && c != '\t' {
			return false
		}
		if c == 0x7f {
			return false
		}
	}
	return true
}
