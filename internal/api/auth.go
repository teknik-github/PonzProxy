package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// DefaultPasswordCost is deliberately above the bcrypt library default. Login
// is rare, and the hash is the only thing standing between a leaked database
// file and an operator's password.
//
// It is a variable rather than a constant so tests can lower it: at this cost
// a single comparison takes the better part of a second, which is correct in
// production and unworkable in a test suite that logs in dozens of times.
const DefaultPasswordCost = 12

// HashPassword produces the stored form of a password at the default cost.
func HashPassword(plain string) (string, error) {
	return hashPasswordAt(plain, DefaultPasswordCost)
}

func hashPasswordAt(plain string, cost int) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), cost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// claims is the payload of a session token.
type claims struct {
	jwt.RegisteredClaims
	Role domain.Role `json:"role"`
}

// issueToken mints a session for a user.
func (s *Server) issueToken(u *domain.User) (string, time.Time, error) {
	expires := time.Now().Add(s.opts.SessionTTL)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprint(u.ID),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expires),
			Issuer:    "ponzproxy",
		},
		Role: u.Role,
	})

	signed, err := token.SignedString(s.opts.JWTSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign session token: %w", err)
	}
	return signed, expires, nil
}

// parseToken validates a session token and returns its claims.
func (s *Server) parseToken(raw string) (*claims, error) {
	var c claims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		return s.opts.JWTSecret, nil
	},
		// Pinning the algorithm is what stops a token signed with "none",
		// or with the public half of an asymmetric key, from being
		// accepted.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("ponzproxy"),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, errors.Join(errUnauthorized, err)
	}
	return &c, nil
}

// principal is the authenticated caller, carried on the request context.
type principal struct {
	UserID int64
	Role   domain.Role
}

type principalKey struct{}

// principalFrom returns the authenticated caller, if any.
func principalFrom(ctx context.Context) (principal, bool) {
	p, ok := ctx.Value(principalKey{}).(principal)
	return p, ok
}

// authenticate rejects requests without a valid session.
//
// The token is accepted from the Authorization header or, for the WebSocket
// endpoint, from a query parameter — browsers cannot set headers on a
// WebSocket handshake.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			writeError(w, s.logger, errUnauthorized)
			return
		}

		c, err := s.parseToken(raw)
		if err != nil {
			writeError(w, s.logger, err)
			return
		}

		var userID int64
		if _, err := fmt.Sscan(c.Subject, &userID); err != nil {
			writeError(w, s.logger, errUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), principalKey{},
			principal{UserID: userID, Role: c.Role})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireWrite rejects viewers from endpoints that change configuration.
func (s *Server) requireWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principalFrom(r.Context())
		if !ok || !p.Role.CanWrite() {
			writeError(w, s.logger, errForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	if header := r.Header.Get("Authorization"); header != "" {
		if token, ok := strings.CutPrefix(header, "Bearer "); ok {
			return strings.TrimSpace(token)
		}
	}
	// Only the WebSocket handshake uses this, where no header can be set.
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

// loginRequest and loginResponse are the shapes of POST /api/auth/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string      `json:"token"`
	ExpiresAt time.Time   `json:"expiresAt"`
	User      domain.User `json:"user"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	source := limiterKey(r)
	if ok, retryAfter := s.logins.allow(source); !ok {
		// Retry-After is in seconds, and a well-behaved client honours it.
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		s.logger.Warn("sign-in refused: too many failures from this address",
			"remote", source, "retryAfter", retryAfter.Round(time.Second))
		writeJSON(w, s.logger, http.StatusTooManyRequests, errorBody{
			Error: "too many failed sign-in attempts; try again later",
		})
		return
	}

	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.logger, err)
		return
	}

	user, err := s.opts.Users.GetByUsername(r.Context(), req.Username)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		writeError(w, s.logger, err)
		return
	}

	// An unknown username still runs a bcrypt comparison, so the response
	// time does not reveal which accounts exist.
	stored := s.decoyHash
	if user != nil {
		stored = user.PasswordHash
	}
	matchErr := bcrypt.CompareHashAndPassword([]byte(stored), []byte(req.Password))

	if user == nil || matchErr != nil {
		s.logins.recordFailure(source)
		s.logger.Warn("failed login", "username", req.Username, "remote", r.RemoteAddr)
		// One message for both cases, for the same reason.
		writeJSON(w, s.logger, http.StatusUnauthorized,
			errorBody{Error: "the username or password is incorrect"})
		return
	}

	// Clearing on success is what keeps an operator who mistypes twice and
	// then succeeds from being locked out by their own next mistake.
	s.logins.recordSuccess(source)

	token, expires, err := s.issueToken(user)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	if err := s.opts.Users.TouchLogin(r.Context(), user.ID, time.Now()); err != nil {
		// Recording the timestamp is a convenience; it must not block a
		// valid login.
		s.logger.Warn("record login time", "error", err)
	}

	s.logger.Info("login", "username", user.Username, "role", user.Role)
	writeJSON(w, s.logger, http.StatusOK, loginResponse{
		Token: token, ExpiresAt: expires, User: *user,
	})
}

// newDecoyHash builds a real bcrypt hash of a random secret at the server's
// own cost. Comparing an unknown username against it makes a failed login take
// the same time as a wrong password, so response timing does not reveal which
// accounts exist.
//
// A hand-written placeholder would not do: bcrypt rejects a malformed hash
// immediately, which is precisely the timing signal being avoided here.
func newDecoyHash(cost int) (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("read entropy: %w", err)
	}
	return hashPasswordAt(string(secret), cost)
}

func (s *Server) handleCurrentUser(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		writeError(w, s.logger, errUnauthorized)
		return
	}
	user, err := s.opts.Users.GetByID(r.Context(), p.UserID)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, user)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		writeError(w, s.logger, errUnauthorized)
		return
	}

	var req changePasswordRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, s.logger, err)
		return
	}

	user, err := s.opts.Users.GetByID(r.Context(), p.UserID)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	// The current password is required even though the caller is already
	// authenticated, so a stolen token alone cannot lock the owner out.
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)) != nil {
		writeJSON(w, s.logger, http.StatusUnauthorized,
			errorBody{Error: "the current password is incorrect"})
		return
	}

	if err := domain.ValidateCredentials(user.Username, req.NewPassword); err != nil {
		writeError(w, s.logger, err)
		return
	}

	hash, err := hashPasswordAt(req.NewPassword, s.passwordCost)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := s.opts.Users.UpdatePassword(r.Context(), user.ID, hash); err != nil {
		writeError(w, s.logger, err)
		return
	}

	s.logger.Info("password changed", "username", user.Username)
	writeJSON(w, s.logger, http.StatusNoContent, nil)
}

// EnsureBootstrapUser creates the initial admin account when the database has
// no users, and returns the generated password so the caller can print it.
//
// A generated password is used rather than a fixed default so an installation
// that is never configured is not trivially reachable.
func EnsureBootstrapUser(ctx context.Context, users domain.UserRepository,
	password string, cost int) (created bool, err error) {
	n, err := users.Count(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}

	hash, err := hashPasswordAt(password, cost)
	if err != nil {
		return false, err
	}
	user := &domain.User{Username: "admin", PasswordHash: hash, Role: domain.RoleAdmin}
	if err := users.Create(ctx, user); err != nil {
		return false, err
	}
	return true, nil
}
