package domain

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// hashFor produces a stored password at bcrypt's minimum cost: these tests
// exercise the decision, not the hash, and the production cost would dominate
// the suite's runtime.
func hashFor(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash %q: %v", password, err)
	}
	return string(hash)
}

func rules(pairs ...string) []AccessRule {
	out := make([]AccessRule, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, AccessRule{Action: AccessAction(pairs[i]), CIDR: pairs[i+1]})
	}
	return out
}

func TestAccessListNormalizeCanonicalises(t *testing.T) {
	l := &AccessList{
		Name: "  office  ",
		Rules: []AccessRule{
			{Action: " ALLOW ", CIDR: " 10.0.0.42/8 "},
			{Action: AccessAllow, CIDR: "10.0.0.0/8"}, // same block once masked
			{Action: AccessDeny, CIDR: "203.0.113.7"}, // bare address
			{Action: "", CIDR: "2001:DB8::/32"},       // action defaults to allow
			{Action: AccessAllow, CIDR: "   "},        // a row left blank
		},
		BasicAuth: []BasicAuthUser{
			{Username: " alice ", PasswordHash: "hash"},
			{Username: "alice", PasswordHash: "other"},
			{},
		},
	}
	l.Normalize()

	if l.Name != "office" {
		t.Errorf("name = %q, want it trimmed", l.Name)
	}

	want := []AccessRule{
		{Action: AccessAllow, CIDR: "10.0.0.0/8"},
		{Action: AccessDeny, CIDR: "203.0.113.7/32"},
		{Action: AccessAllow, CIDR: "2001:db8::/32"},
	}
	if len(l.Rules) != len(want) {
		t.Fatalf("rules = %+v, want %d entries", l.Rules, len(want))
	}
	for i, w := range want {
		if l.Rules[i].Action != w.Action || l.Rules[i].CIDR != w.CIDR {
			t.Errorf("rule %d = %+v, want %+v", i, l.Rules[i], w)
		}
	}

	if len(l.BasicAuth) != 1 || l.BasicAuth[0].Username != "alice" {
		t.Errorf("basic auth = %+v, want one trimmed entry", l.BasicAuth)
	}

	// Normalising twice must not change anything again.
	before := l.Rules[0].CIDR
	l.Normalize()
	if l.Rules[0].CIDR != before || len(l.Rules) != len(want) {
		t.Errorf("second Normalize changed the list: %+v", l.Rules)
	}
}

func TestAccessListValidate(t *testing.T) {
	hash := hashFor(t, "secret")

	cases := []struct {
		name  string
		list  AccessList
		field string // "" when the list must validate
	}{
		{
			name: "allow rule only",
			list: AccessList{Name: "office", Rules: rules("allow", "10.0.0.0/8")},
		},
		{
			name: "basic auth only",
			list: AccessList{Name: "staff",
				BasicAuth: []BasicAuthUser{{Username: "alice", PasswordHash: hash}}},
		},
		{
			name: "satisfy any with both halves",
			list: AccessList{Name: "both", SatisfyAny: true,
				Rules:     rules("allow", "10.0.0.0/8"),
				BasicAuth: []BasicAuthUser{{Username: "alice", PasswordHash: hash}}},
		},
		{
			name:  "no name",
			list:  AccessList{Rules: rules("allow", "10.0.0.0/8")},
			field: "name",
		},
		{
			name:  "malformed cidr",
			list:  AccessList{Name: "bad", Rules: rules("allow", "10.0.0.0/33")},
			field: "rules[0].cidr",
		},
		{
			name:  "cidr that is not an address at all",
			list:  AccessList{Name: "bad", Rules: rules("allow", "not-an-address")},
			field: "rules[0].cidr",
		},
		{
			name:  "unknown action",
			list:  AccessList{Name: "bad", Rules: rules("maybe", "10.0.0.0/8")},
			field: "rules[0].action",
		},
		{
			name: "username with a colon",
			list: AccessList{Name: "bad",
				BasicAuth: []BasicAuthUser{{Username: "al:ice", PasswordHash: hash}}},
			field: "basicAuth[0].username",
		},
		{
			name: "user without a password",
			list: AccessList{Name: "bad",
				BasicAuth: []BasicAuthUser{{Username: "alice"}}},
			field: "basicAuth[0].password",
		},
		{
			name:  "restricts nothing",
			list:  AccessList{Name: "empty"},
			field: "rules",
		},
		{
			name: "satisfy any without credentials",
			list: AccessList{Name: "half", SatisfyAny: true,
				Rules: rules("allow", "10.0.0.0/8")},
			field: "satisfyAny",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := tc.list
			// Normalize is deliberately not called: Validate must stand on
			// its own for input that never went through a handler.
			err := list.Validate()
			if tc.field == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want no error", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want a validation error")
			}
			if _, ok := fieldsOf(t, err)[tc.field]; !ok {
				t.Errorf("fields = %v, want one naming %q", fieldsOf(t, err), tc.field)
			}
		})
	}
}

func TestEvaluateAccess(t *testing.T) {
	hash := hashFor(t, "opensesame")
	users := []BasicAuthUser{{Username: "alice", PasswordHash: hash}}

	good := BasicCredentials{Username: "alice", Password: "opensesame", Present: true}
	wrongPassword := BasicCredentials{Username: "alice", Password: "nope", Present: true}
	unknownUser := BasicCredentials{Username: "mallory", Password: "opensesame", Present: true}
	none := BasicCredentials{}

	cases := []struct {
		name  string
		list  *AccessList
		ip    string
		creds BasicCredentials
		want  AccessDecision
	}{
		{
			name: "no list at all",
			list: nil, ip: "203.0.113.9", want: AccessGranted,
		},
		{
			name: "empty rule set admits every address",
			list: &AccessList{}, ip: "203.0.113.9", want: AccessGranted,
		},
		{
			name: "address inside an allow rule",
			list: &AccessList{Rules: rules("allow", "10.0.0.0/8")},
			ip:   "10.4.3.2", want: AccessGranted,
		},
		{
			name: "address outside every allow rule",
			list: &AccessList{Rules: rules("allow", "10.0.0.0/8")},
			ip:   "192.0.2.5", want: AccessRefused,
		},
		{
			name: "deny overrides a matching allow",
			list: &AccessList{Rules: rules("allow", "10.0.0.0/8", "deny", "10.4.0.0/16")},
			ip:   "10.4.3.2", want: AccessRefused,
		},
		{
			name: "deny listed before the allow still wins",
			list: &AccessList{Rules: rules("deny", "10.4.0.0/16", "allow", "10.0.0.0/8")},
			ip:   "10.4.3.2", want: AccessRefused,
		},
		{
			name: "deny rules alone leave everything else allowed",
			list: &AccessList{Rules: rules("deny", "10.4.0.0/16")},
			ip:   "192.0.2.5", want: AccessGranted,
		},
		{
			name: "single host rule",
			list: &AccessList{Rules: rules("allow", "203.0.113.7/32")},
			ip:   "203.0.113.7", want: AccessGranted,
		},
		{
			name: "single host rule does not cover its neighbour",
			list: &AccessList{Rules: rules("allow", "203.0.113.7/32")},
			ip:   "203.0.113.8", want: AccessRefused,
		},
		{
			name: "ipv6 inside an allow prefix",
			list: &AccessList{Rules: rules("allow", "2001:db8::/32")},
			ip:   "2001:db8:dead:beef::1", want: AccessGranted,
		},
		{
			name: "ipv6 outside every allow prefix",
			list: &AccessList{Rules: rules("allow", "2001:db8::/32")},
			ip:   "2001:db9::1", want: AccessRefused,
		},
		{
			name: "ipv6 deny overrides a wider ipv6 allow",
			list: &AccessList{Rules: rules("allow", "2001:db8::/32", "deny", "2001:db8:dead::/48")},
			ip:   "2001:db8:dead:beef::1", want: AccessRefused,
		},
		{
			name: "ipv4 client does not match an ipv6 rule",
			list: &AccessList{Rules: rules("allow", "::/0")},
			ip:   "10.0.0.1", want: AccessRefused,
		},
		{
			name: "v4-mapped client matches the ipv4 rule it really is",
			list: &AccessList{Rules: rules("allow", "10.0.0.0/8")},
			ip:   "::ffff:10.0.0.1", want: AccessGranted,
		},
		{
			name: "unparseable client address is refused",
			list: &AccessList{Rules: rules("allow", "0.0.0.0/0")},
			ip:   "not-an-ip", want: AccessRefused,
		},
		{
			name: "a corrupted rule refuses rather than being skipped",
			list: &AccessList{Rules: rules("allow", "10.0.0.0/99")},
			ip:   "10.0.0.1", want: AccessRefused,
		},

		// Basic auth, with no address rules in the way.
		{
			name: "correct credentials",
			list: &AccessList{BasicAuth: users},
			ip:   "192.0.2.5", creds: good, want: AccessGranted,
		},
		{
			name: "no credentials offered",
			list: &AccessList{BasicAuth: users},
			ip:   "192.0.2.5", creds: none, want: AccessNeedsCredentials,
		},
		{
			name: "wrong password",
			list: &AccessList{BasicAuth: users},
			ip:   "192.0.2.5", creds: wrongPassword, want: AccessNeedsCredentials,
		},
		{
			name: "unknown username",
			list: &AccessList{BasicAuth: users},
			ip:   "192.0.2.5", creds: unknownUser, want: AccessNeedsCredentials,
		},

		// Both halves configured.
		{
			name: "satisfy all needs the password even from an allowed address",
			list: &AccessList{Rules: rules("allow", "10.0.0.0/8"), BasicAuth: users},
			ip:   "10.0.0.1", creds: none, want: AccessNeedsCredentials,
		},
		{
			name: "satisfy all grants when both halves pass",
			list: &AccessList{Rules: rules("allow", "10.0.0.0/8"), BasicAuth: users},
			ip:   "10.0.0.1", creds: good, want: AccessGranted,
		},
		{
			name: "satisfy all refuses a foreign address outright, credentials or not",
			list: &AccessList{Rules: rules("allow", "10.0.0.0/8"), BasicAuth: users},
			ip:   "192.0.2.5", creds: good, want: AccessRefused,
		},
		{
			name: "satisfy any lets an allowed address in without a password",
			list: &AccessList{SatisfyAny: true,
				Rules: rules("allow", "10.0.0.0/8"), BasicAuth: users},
			ip: "10.0.0.1", creds: none, want: AccessGranted,
		},
		{
			name: "satisfy any lets a correct password in from anywhere",
			list: &AccessList{SatisfyAny: true,
				Rules: rules("allow", "10.0.0.0/8"), BasicAuth: users},
			ip: "192.0.2.5", creds: good, want: AccessGranted,
		},
		{
			name: "satisfy any still prompts a foreign address with no credentials",
			list: &AccessList{SatisfyAny: true,
				Rules: rules("allow", "10.0.0.0/8"), BasicAuth: users},
			ip: "192.0.2.5", creds: none, want: AccessNeedsCredentials,
		},
		{
			name: "satisfy any admits a denied address holding the password",
			list: &AccessList{SatisfyAny: true,
				Rules: rules("deny", "192.0.2.0/24"), BasicAuth: users},
			ip: "192.0.2.5", creds: good, want: AccessGranted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateAccess(tc.list, tc.ip, tc.creds); got != tc.want {
				t.Errorf("EvaluateAccess(%s) = %s, want %s", tc.ip, got, tc.want)
			}
		})
	}
}

// TestEvaluateAccessUsesNormalizedRules covers the path real data takes: the
// operator's spelling of a rule reaches evaluation only after Normalize.
func TestEvaluateAccessUsesNormalizedRules(t *testing.T) {
	l := &AccessList{Name: "office", Rules: rules("allow", "10.1.2.3/16")}
	l.Normalize()
	if err := l.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if got := EvaluateAccess(l, "10.1.9.9", BasicCredentials{}); got != AccessGranted {
		t.Errorf("host bits in the rule changed the block: got %s", got)
	}
	if got := EvaluateAccess(l, "10.2.0.1", BasicCredentials{}); got != AccessRefused {
		t.Errorf("address outside the block = %s, want refused", got)
	}
}
