// Package guardian inspects requests for obvious attack patterns before they
// reach an upstream.
//
// It is deliberately not a general web application firewall. Everything here
// runs on the request path, so the checks are substring scans over a bounded
// amount of text — no regular expressions, whose backtracking is itself a
// denial-of-service lever, and no body inspection, which would mean buffering
// requests the proxy currently streams.
//
// The bar for a rule is that a match should be almost certainly hostile. A
// firewall that occasionally refuses real customers is worse than none,
// because the failure is visible and the protection is not.
package guardian

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// maxScan bounds how much of any single value is examined. A client controls
// the path and the query, and scanning megabytes per request would hand it a
// cheap way to burn proxy CPU.
const maxScan = 4096

// Verdict is the outcome of inspecting one request.
type Verdict struct {
	// Matched reports whether any rule fired. A request can match in
	// detect mode without being blocked.
	Matched bool
	Rule    domain.GuardianRule
	// Detail names what matched, for the log and the console. It is the
	// pattern, never the full request, so a log line cannot be made to
	// carry a payload of the attacker's choosing.
	Detail string
}

// Inspect examines a request against the host's settings.
//
// It returns on the first match: knowing a request is hostile is enough, and
// continuing would only cost time on exactly the requests least worth spending
// it on.
func Inspect(g *domain.Guardian, r *http.Request) Verdict {
	if !g.Enabled() {
		return Verdict{}
	}

	// Measured, never assembled: concatenating path and query to take one
	// length would allocate a copy of whatever the client sent, before any
	// of the bounds below have had a chance to apply.
	targetLen := len(r.URL.EscapedPath())
	if r.URL.RawQuery != "" {
		targetLen += 1 + len(r.URL.RawQuery)
	}
	if g.MaxURILength > 0 && targetLen > g.MaxURILength {
		return Verdict{
			Matched: true,
			Rule:    domain.RulePathTraversal,
			Detail:  "request target is longer than the configured limit",
		}
	}

	// Path and query are decoded by different rules, and conflating them
	// is an evasion: "+" means a space in a query string but a literal
	// plus in a path, so a filter that only path-decodes never sees
	// "union select" in "?q=union+select".
	// Clamped before decoding, not after. Decoding first would mean two
	// full unescape passes over whatever the client sent — measured at
	// 150us for a 16KB target against 3us for an ordinary one, which is a
	// CPU amplification lever handed to exactly the wrong people.
	path := strings.ToLower(decodeTwice(clamp(r.URL.EscapedPath()), url.PathUnescape))
	query := strings.ToLower(decodeTwice(clamp(r.URL.RawQuery), url.QueryUnescape))

	decoded := path
	if query != "" {
		decoded += "?" + query
	}

	if g.Has(domain.RuleControlCharacters) {
		if v := checkControlCharacters(decoded); v.Matched {
			return v
		}
	}
	if g.Has(domain.RulePathTraversal) {
		if v := checkPatterns(path, traversalPatterns, domain.RulePathTraversal); v.Matched {
			return v
		}
	}
	if g.Has(domain.RuleSensitiveFiles) {
		if v := checkSensitiveFiles(path); v.Matched {
			return v
		}
	}
	if g.Has(domain.RuleScannerAgents) {
		if v := checkScannerAgent(r.UserAgent()); v.Matched {
			return v
		}
	}
	if g.Has(domain.RuleSQLInjection) {
		if v := checkPatterns(decoded, sqlPatterns, domain.RuleSQLInjection); v.Matched {
			return v
		}
	}
	if g.Has(domain.RuleShellInjection) {
		if v := checkPatterns(decoded, shellPatterns, domain.RuleShellInjection); v.Matched {
			return v
		}
	}

	return Verdict{}
}

// traversalPatterns are the forms of "go up a directory" that survive
// decoding. The literal "../" is checked too because the path may never have
// been encoded at all.
var traversalPatterns = []string{"../", "..\\", "/..", "\\..", "....//"}

// sensitiveFiles are paths that no application should ever serve. They are
// matched as substrings of the path so "/static/.env" is caught as well as
// "/.env" — a real request never contains any of them.
var sensitiveFiles = []string{
	"/.env", "/.git/", "/.gitignore", "/.svn/", "/.hg/",
	"/.aws/", "/.ssh/", "/id_rsa", "/.htpasswd", "/.htaccess",
	"/web.config", "/.ds_store", "/.npmrc", "/.dockerenv",
	"/wp-config.php", "/config.php.bak", "/database.sql", "/dump.sql",
	"/.vscode/", "/.idea/", "/composer.lock", "/package-lock.json.bak",
}

// sqlPatterns are fragments that appear in injection attempts and almost never
// in legitimate input. Single words like "select" are deliberately absent:
// they match ordinary search queries.
var sqlPatterns = []string{
	"union select", "union all select", "' or '1'='1", "\" or \"1\"=\"1",
	" or 1=1", "' or 1=1", "'; drop table", "; drop table",
	"information_schema", "sleep(", "benchmark(", "waitfor delay",
	"pg_sleep(", "load_file(", "into outfile", "xp_cmdshell",
}

// shellPatterns are command-chaining attempts. Bare ";" and "|" are absent
// because they appear in ordinary query strings.
var shellPatterns = []string{
	";cat ", "|cat ", "&&cat ", "; cat /etc", "|| cat /etc",
	"/etc/passwd", "/etc/shadow", "$(", "`id`", ";id;", "|id",
	"wget http", "curl http", "nc -e", "bash -i", "/bin/sh",
}

// scannerAgents announce themselves in the User-Agent header. Blocking them
// stops only the unsophisticated, which is nonetheless most of the volume.
var scannerAgents = []string{
	"sqlmap", "nikto", "nmap", "masscan", "zgrab", "nessus",
	"acunetix", "netsparker", "wpscan", "dirbuster", "gobuster",
	"havij", "arachni", "metasploit",
}

func checkPatterns(haystack string, patterns []string, rule domain.GuardianRule) Verdict {
	for _, p := range patterns {
		if strings.Contains(haystack, p) {
			return Verdict{Matched: true, Rule: rule, Detail: "matched " + p}
		}
	}
	return Verdict{}
}

func checkSensitiveFiles(path string) Verdict {
	for _, f := range sensitiveFiles {
		if strings.Contains(path, f) {
			return Verdict{
				Matched: true,
				Rule:    domain.RuleSensitiveFiles,
				Detail:  "probed for " + f,
			}
		}
	}
	return Verdict{}
}

// checkControlCharacters rejects bytes below space and the DEL byte. No
// legitimate client puts them in a request target; they are used to confuse
// whatever parses the request after the proxy does.
func checkControlCharacters(target string) Verdict {
	for i := range len(target) {
		if c := target[i]; c < 0x20 || c == 0x7f {
			return Verdict{
				Matched: true,
				Rule:    domain.RuleControlCharacters,
				Detail:  "control character in the request target",
			}
		}
	}
	return Verdict{}
}

func checkScannerAgent(agent string) Verdict {
	if agent == "" {
		return Verdict{}
	}
	lowered := strings.ToLower(clamp(agent))
	for _, s := range scannerAgents {
		if strings.Contains(lowered, s) {
			return Verdict{
				Matched: true,
				Rule:    domain.RuleScannerAgents,
				Detail:  "user agent identifies as " + s,
			}
		}
	}
	return Verdict{}
}

// decodeTwice resolves percent-encoding up to two rounds, which is what
// catches the doubly-encoded traversal that gets past filters checking the raw
// value only. Two is enough: a legitimate URL never needs three.
//
// The unescape function is passed in because the rules differ — see Inspect.
// Undecodable input is returned as far as it got: a malformed escape is not a
// reason to stop inspecting, which is exactly what an attacker would want.
func decodeTwice(s string, unescape func(string) (string, error)) string {
	for range 2 {
		decoded, err := unescape(s)
		if err != nil || decoded == s {
			return s
		}
		s = decoded
	}
	return s
}

// clamp bounds the text any single check examines, trimming back off a
// percent-escape the cut would otherwise split — an unescape that fails
// returns the string undecoded, which would silently disable decoding for
// every long request.
func clamp(s string) string {
	if len(s) <= maxScan {
		return s
	}
	s = s[:maxScan]
	for i := len(s) - 1; i >= 0 && i > len(s)-3; i-- {
		if s[i] == '%' {
			return s[:i]
		}
	}
	return s
}
