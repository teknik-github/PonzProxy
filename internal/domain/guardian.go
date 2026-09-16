package domain

import "strings"

// GuardianMode is Mode under the name the guardian's own code and the API
// payload use. It is an alias rather than a distinct type because the two
// really are the same choice with the same consequences — see Mode.
type GuardianMode = Mode

const (
	// GuardianOff does no inspection at all.
	GuardianOff = ModeOff
	// GuardianDetect records matches and forwards the request anyway.
	GuardianDetect = ModeDetect
	// GuardianBlock refuses matched requests with 403.
	GuardianBlock = ModeBlock
)

// GuardianRule names one family of checks. They are separate switches because
// their false-positive risk differs by an order of magnitude: refusing a path
// containing "../" is nearly always right, while refusing one containing
// "union select" will eventually catch a search box.
type GuardianRule string

const (
	// RulePathTraversal catches attempts to climb out of the web root,
	// including percent-encoded and doubly-encoded forms.
	RulePathTraversal GuardianRule = "path_traversal"
	// RuleSensitiveFiles catches probes for files that should never be
	// served: .env, .git, cloud credentials, database dumps.
	RuleSensitiveFiles GuardianRule = "sensitive_files"
	// RuleSQLInjection catches SQL fragments in the path or query.
	RuleSQLInjection GuardianRule = "sql_injection"
	// RuleShellInjection catches shell metacharacters used to chain
	// commands.
	RuleShellInjection GuardianRule = "shell_injection"
	// RuleScannerAgents refuses tools that announce themselves. It stops
	// only the lazy, which is still most of the noise.
	RuleScannerAgents GuardianRule = "scanner_agents"
	// RuleControlCharacters catches null bytes and other control bytes in
	// a path, which no legitimate client sends and which are used to
	// confuse whatever parses the request next.
	RuleControlCharacters GuardianRule = "control_characters"
)

// GuardianRules lists every rule, for the UI's picker.
func GuardianRules() []GuardianRule {
	return []GuardianRule{
		RulePathTraversal, RuleSensitiveFiles, RuleControlCharacters,
		RuleScannerAgents, RuleSQLInjection, RuleShellInjection,
	}
}

func (r GuardianRule) Valid() bool {
	for _, known := range GuardianRules() {
		if r == known {
			return true
		}
	}
	return false
}

// SafeByDefault reports whether a rule is quiet enough to enable without
// watching it first. The two injection rules are not: they match on content a
// real application may legitimately receive.
func (r GuardianRule) SafeByDefault() bool {
	switch r {
	case RuleSQLInjection, RuleShellInjection:
		return false
	default:
		return true
	}
}

// Guardian is the per-host request inspection setting.
type Guardian struct {
	Mode  GuardianMode   `json:"mode"`
	Rules []GuardianRule `json:"rules"`
	// MaxURILength refuses absurdly long request targets outright. Zero
	// disables the check.
	MaxURILength int `json:"maxUriLength"`
}

// DefaultGuardian is applied to new hosts. It is off: turning request
// inspection on for someone's existing traffic without being asked is how a
// proxy earns a reputation for breaking things.
func DefaultGuardian() Guardian {
	rules := make([]GuardianRule, 0, len(GuardianRules()))
	for _, r := range GuardianRules() {
		if r.SafeByDefault() {
			rules = append(rules, r)
		}
	}
	return Guardian{Mode: GuardianOff, Rules: rules, MaxURILength: 2048}
}

// Enabled reports whether any inspection happens at all.
func (g *Guardian) Enabled() bool { return g.Mode == GuardianDetect || g.Mode == GuardianBlock }

// Has reports whether a rule is switched on.
func (g *Guardian) Has(r GuardianRule) bool {
	for _, enabled := range g.Rules {
		if enabled == r {
			return true
		}
	}
	return false
}

// Normalize fills in defaults and canonicalises operator input.
func (g *Guardian) Normalize() {
	g.Mode = NormalizeMode(g.Mode)
	if g.MaxURILength < 0 {
		g.MaxURILength = 0
	}

	rules := make([]GuardianRule, 0, len(g.Rules))
	seen := make(map[GuardianRule]struct{}, len(g.Rules))
	for _, r := range g.Rules {
		r = GuardianRule(strings.ToLower(strings.TrimSpace(string(r))))
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		rules = append(rules, r)
	}
	g.Rules = rules
}

// Validate checks the setting as a whole.
func (g *Guardian) Validate() error {
	v := &ValidationError{}

	if !g.Mode.Valid() {
		v.Add("guardian.mode", "must be off, detect or block")
	}
	for _, r := range g.Rules {
		if !r.Valid() {
			v.Add("guardian.rules", "%q is not a known rule", string(r))
		}
	}
	if g.Enabled() && len(g.Rules) == 0 {
		v.Add("guardian.rules", "choose at least one rule, or inspection can never match")
	}
	if g.MaxURILength != 0 && (g.MaxURILength < 256 || g.MaxURILength > 65536) {
		v.Add("guardian.maxUriLength", "must be between 256 and 65536, or 0 to disable")
	}

	return v.Err()
}
