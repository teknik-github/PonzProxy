package domain

import (
	"strings"
	"time"
)

// Role decides what an authenticated operator may do. Roles are coarse on
// purpose: this is an appliance UI, not a multi-tenant product.
type Role string

const (
	// RoleAdmin may change any configuration and manage users.
	RoleAdmin Role = "admin"
	// RoleViewer may read configuration and watch live metrics only.
	RoleViewer Role = "viewer"
)

func (r Role) Valid() bool { return r == RoleAdmin || r == RoleViewer }

// CanWrite reports whether the role may mutate configuration.
func (r Role) CanWrite() bool { return r == RoleAdmin }

// User is an operator account for the control plane.
type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	Role         Role       `json:"role"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
}

// NormalizeUsername lowercases and trims so logins are case-insensitive and
// the uniqueness constraint cannot be sidestepped by capitalisation.
func NormalizeUsername(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ValidateCredentials checks a username and a plaintext password before the
// password is hashed. MinPasswordLength is enforced here so every caller —
// bootstrap, API, password change — applies the same rule.
const (
	MinPasswordLength = 8
	// MaxPasswordLength is bcrypt's input limit. Anything past it is
	// ignored by the hash, so it is rejected instead.
	MaxPasswordLength = 72
)

func ValidateCredentials(username, password string) error {
	v := &ValidationError{}
	if len(username) < 3 || len(username) > 32 {
		v.Add("username", "must be between 3 and 32 characters")
	}
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			v.Add("username", "may only contain a-z, 0-9, dot, hyphen and underscore")
		}
	}
	if len(password) < MinPasswordLength {
		v.Add("password", "must be at least %d characters", MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		// bcrypt only reads the first 72 bytes. Rejecting longer input is
		// better than accepting a password whose tail is never checked.
		v.Add("password", "must be at most %d bytes", MaxPasswordLength)
	}
	return v.Err()
}
