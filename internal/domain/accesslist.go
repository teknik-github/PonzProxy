package domain

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// AccessAction is what a rule does when the client address falls inside its
// CIDR block.
type AccessAction string

const (
	// AccessAllow admits an address, unless a deny rule also covers it.
	AccessAllow AccessAction = "allow"
	// AccessDeny refuses an address outright.
	AccessDeny AccessAction = "deny"
)

// Valid reports whether a is one of the supported actions.
func (a AccessAction) Valid() bool { return a == AccessAllow || a == AccessDeny }

// AccessRule matches one CIDR block against the client address.
type AccessRule struct {
	ID           int64        `json:"id"`
	AccessListID int64        `json:"accessListId"`
	Action       AccessAction `json:"action"`
	// CIDR is kept in masked form ("10.0.0.0/8"). Normalize also accepts a
	// bare address and widens it to a single-host prefix.
	CIDR string `json:"cidr"`
}

// BasicAuthUser is one credential an access list accepts. PasswordHash is
// withheld from API responses for the same reason a private key is: it is
// there to be compared against, not read back.
type BasicAuthUser struct {
	ID           int64  `json:"id"`
	AccessListID int64  `json:"accessListId"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
}

// AccessList is a named rule set a Host can put in front of its upstreams.
// A request that fails evaluation is refused before any upstream is picked.
type AccessList struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`

	// SatisfyAny relaxes the two halves from AND to OR: with it set, an
	// address the rules admit needs no password, and a correct password is
	// accepted from any address.
	SatisfyAny bool `json:"satisfyAny"`

	Rules     []AccessRule    `json:"rules"`
	BasicAuth []BasicAuthUser `json:"basicAuth"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AccessDecision is the outcome of evaluating one request against a list.
type AccessDecision int

const (
	// AccessGranted lets the request continue to upstream selection.
	AccessGranted AccessDecision = iota
	// AccessRefused is a flat refusal: 403, with no hint that credentials
	// might help, because they would not.
	AccessRefused
	// AccessNeedsCredentials means basic auth could still admit this
	// request: 401 with a WWW-Authenticate challenge.
	AccessNeedsCredentials
)

func (d AccessDecision) String() string {
	switch d {
	case AccessGranted:
		return "granted"
	case AccessNeedsCredentials:
		return "needs credentials"
	default:
		return "refused"
	}
}

// BasicCredentials is the pair carried by an Authorization: Basic header.
// Present separates a request that sent no header from one that sent an
// empty username and password.
type BasicCredentials struct {
	Username string
	Password string
	Present  bool
	// Verified lets a caller assert that this exact pair has already been
	// checked against the stored hash. bcrypt is deliberately slow, and on
	// a proxy hot path that cost lands on every request; the proxy caches
	// recent verifications and sets this so the rest of the decision —
	// address rules, the satisfy-any combination — still runs normally.
	//
	// Only a caller holding a positive result for this same list version
	// may set it. Setting it otherwise skips the password check entirely.
	Verified bool
}

// EvaluateAccess decides one request, and is the only place that decision is
// made: the proxy calls it, the tests exercise it, and nothing re-derives it.
//
// The order is fixed:
//
//  1. The address rules run first. A matching deny rule refuses the address
//     whatever else matches it, so deny always overrides allow. With no deny
//     match the address passes when an allow rule covers it — or when the
//     list has no allow rule at all, in which case the rules only subtract.
//     An empty rule set therefore passes every address.
//  2. Basic auth runs second. With no credentials configured this half
//     passes vacuously; otherwise the request must carry a username that is
//     configured and a password matching that user's stored hash.
//  3. SatisfyAny combines them: either half is enough when it is set, both
//     are required when it is not.
//
// The default, when nothing matches at all, is to allow — which only a list
// that restricts nothing can reach, and Validate refuses to store one of
// those. A nil list means the host has none, which is also allowed.
func EvaluateAccess(l *AccessList, clientIP string, creds BasicCredentials) AccessDecision {
	if l == nil {
		return AccessGranted
	}

	addressOK := l.allowsAddress(clientIP)
	authConfigured := len(l.BasicAuth) > 0

	// Anything the address already settles is answered without touching
	// bcrypt, which is deliberately slow and runs on the request path.
	switch {
	case l.SatisfyAny && addressOK:
		return AccessGranted
	case !l.SatisfyAny && !addressOK:
		// Under satisfy-all no password can rescue a refused address, so
		// prompting for one would only be misleading.
		return AccessRefused
	case !authConfigured:
		if addressOK {
			return AccessGranted
		}
		return AccessRefused
	}

	if l.acceptsCredentials(creds) {
		return AccessGranted
	}
	return AccessNeedsCredentials
}

// allowsAddress applies the CIDR rules. See EvaluateAccess for the contract.
func (l *AccessList) allowsAddress(clientIP string) bool {
	if len(l.Rules) == 0 {
		return true
	}

	addr, err := netip.ParseAddr(clientIP)
	if err != nil {
		// Rules exist and cannot be applied: refuse rather than guess.
		return false
	}
	// A v4 client reaching a dual-stack listener is seen as ::ffff:a.b.c.d,
	// which no IPv4 prefix would contain.
	addr = addr.Unmap()

	hasAllow, matchedAllow := false, false
	for _, r := range l.Rules {
		prefix, err := netip.ParsePrefix(r.CIDR)
		if err != nil {
			// Validate rejects these before they can be stored, so a
			// malformed rule here means a corrupted row. Refusing is the
			// only safe reading of a rule that might have been a deny.
			return false
		}
		if r.Action == AccessAllow {
			hasAllow = true
		}
		if !prefix.Contains(addr) {
			continue
		}
		if r.Action == AccessDeny {
			return false
		}
		matchedAllow = true
	}

	// A list of deny rules alone only subtracts; the first allow rule turns
	// the list into a whitelist.
	return !hasAllow || matchedAllow
}

// acceptsCredentials reports whether the pair matches a configured user.
func (l *AccessList) acceptsCredentials(creds BasicCredentials) bool {
	if !creds.Present {
		return false
	}
	if creds.Verified {
		// The caller has already compared this pair against the stored
		// hash for this version of the list.
		return true
	}
	for _, u := range l.BasicAuth {
		// An unknown username is rejected without a hash comparison. Unlike
		// the operator accounts in api/auth.go this is not an account
		// database — the usernames protect nothing on their own, and the
		// list they belong to is often published to the people using it.
		if u.Username != creds.Username {
			continue
		}
		return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(creds.Password)) == nil
	}
	return false
}

// Normalize fills in defaults and canonicalises operator input so validation,
// storage and evaluation all see the same shape. It is safe to call twice.
func (l *AccessList) Normalize() {
	l.Name = strings.TrimSpace(l.Name)

	rules := make([]AccessRule, 0, len(l.Rules))
	seenRule := make(map[string]struct{}, len(l.Rules))
	for _, r := range l.Rules {
		r.Action = AccessAction(strings.ToLower(strings.TrimSpace(string(r.Action))))
		if r.Action == "" {
			r.Action = AccessAllow
		}
		r.CIDR = normalizeCIDR(r.CIDR)
		if r.CIDR == "" {
			continue // a row the operator left blank
		}
		key := string(r.Action) + " " + r.CIDR
		if _, dup := seenRule[key]; dup {
			continue
		}
		seenRule[key] = struct{}{}
		rules = append(rules, r)
	}
	l.Rules = rules

	users := make([]BasicAuthUser, 0, len(l.BasicAuth))
	seenUser := make(map[string]struct{}, len(l.BasicAuth))
	for _, u := range l.BasicAuth {
		u.Username = strings.TrimSpace(u.Username)
		if u.Username == "" && u.PasswordHash == "" {
			continue
		}
		if _, dup := seenUser[u.Username]; dup {
			continue
		}
		seenUser[u.Username] = struct{}{}
		users = append(users, u)
	}
	l.BasicAuth = users
}

// Validate reports every problem with the list at once. Call Normalize first.
func (l *AccessList) Validate() error {
	v := &ValidationError{}

	if l.Name == "" {
		v.Add("name", "is required")
	} else if len(l.Name) > 100 {
		v.Add("name", "must be at most 100 characters")
	}

	for i, r := range l.Rules {
		field := "rules[" + strconv.Itoa(i) + "]"
		if !r.Action.Valid() {
			v.Add(field+".action", "%q is not a supported action; use allow or deny", string(r.Action))
		}
		if _, err := netip.ParsePrefix(r.CIDR); err != nil {
			v.Add(field+".cidr",
				"must be an address or a CIDR block, e.g. 10.0.0.0/8, 203.0.113.7 or 2001:db8::/32")
		}
	}

	for i, u := range l.BasicAuth {
		field := "basicAuth[" + strconv.Itoa(i) + "]"
		switch {
		case u.Username == "":
			v.Add(field+".username", "is required")
		case len(u.Username) > 64:
			v.Add(field+".username", "must be at most 64 characters")
		case strings.ContainsRune(u.Username, ':'):
			// RFC 7617 splits the credential on the first colon, so a
			// username containing one can never be sent back.
			v.Add(field+".username", "may not contain a colon")
		}
		if u.PasswordHash == "" {
			v.Add(field+".password", "is required")
		}
	}

	// The two rules below exist because the permissive defaults in
	// EvaluateAccess are only safe while each half actually restricts
	// something. A list that reaches the request path without doing so
	// would silently admit everything the operator meant to keep out.
	if len(l.Rules) == 0 && len(l.BasicAuth) == 0 {
		v.Add("rules", "an access list needs at least one rule or one user, or it would admit every request")
	}
	if l.SatisfyAny && (len(l.Rules) == 0 || len(l.BasicAuth) == 0) {
		v.Add("satisfyAny",
			"needs both address rules and users, since with only one of them either half alone would admit every request")
	}

	return v.Err()
}

// normalizeCIDR canonicalises a rule's address.
//
// A bare address is widened to a single-host prefix, which is what an
// operator typing one address means, and a prefix is stored masked so the
// same block cannot be saved under two spellings. Input that parses as
// neither is returned unchanged, for Validate to report.
func normalizeCIDR(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()).String()
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked().String()
	}
	return s
}

// AccessListRepository stores access lists with their rules and users. Like
// HostRepository, writes must be atomic across all three tables: a list is
// never visible without the rules that give it meaning.
type AccessListRepository interface {
	// List returns every access list, children included, ordered by name.
	List(ctx context.Context) ([]AccessList, error)
	// Get returns ErrNotFound when no list has the given id.
	Get(ctx context.Context, id int64) (*AccessList, error)
	Create(ctx context.Context, l *AccessList) error
	// Update replaces the list and both child sets wholesale.
	Update(ctx context.Context, l *AccessList) error
	// Delete returns ErrInUse while a host still references the list.
	Delete(ctx context.Context, id int64) error
	// HostsUsing counts the hosts pointing at the list. It asks about
	// hosts but belongs to this aggregate's lifecycle: nothing reads it
	// except the code deciding whether a list may be deleted, and the UI
	// warning that says so.
	HostsUsing(ctx context.Context, id int64) (int, error)
}
