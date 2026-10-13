package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vipul2902/clouddeploy/internal/audit"
	"github.com/vipul2902/clouddeploy/internal/slug"
)

// Domain errors mapped to HTTP responses by the handler.
var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidRefresh     = errors.New("invalid refresh token")
	// ErrRefreshReuse means a revoked refresh token was presented: the token
	// was likely stolen, so its whole family has been revoked.
	ErrRefreshReuse = errors.New("refresh token reuse detected")
)

// Password length bounds. The upper bound stops attackers from submitting
// megabyte passwords to burn CPU in argon2.
const (
	minPasswordLen = 12
	maxPasswordLen = 128
)

// Service implements authentication use cases.
type Service struct {
	store      store
	tokens     *TokenIssuer
	refreshTTL time.Duration
	// dummyHash is verified against when the email does not exist, so login
	// takes the same time whether or not an account exists (no enumeration).
	dummyHash string
}

// NewService builds the auth service.
func NewService(pool *pgxpool.Pool, tokens *TokenIssuer, refreshTTL time.Duration) (*Service, error) {
	dummy, err := HashPassword("timing-equalization-placeholder")
	if err != nil {
		return nil, err
	}
	return &Service{store: store{pool: pool}, tokens: tokens, refreshTTL: refreshTTL, dummyHash: dummy}, nil
}

// TokenPair is returned on register, login and refresh.
type TokenPair struct {
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_token_expires_at"`
	RefreshToken     string    `json:"refresh_token,omitempty"` // omitted in cookie mode
	RefreshExpiresAt time.Time `json:"refresh_token_expires_at"`
	TokenType        string    `json:"token_type"`
}

// User is the public view of a user.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Membership is a user's role in one organization.
type Membership struct {
	OrganizationID   string `json:"organization_id"`
	OrganizationName string `json:"organization_name"`
	OrganizationSlug string `json:"organization_slug"`
	Role             Role   `json:"role"`
}

// RegisterInput is the registration request.
type RegisterInput struct {
	Email            string `json:"email"`
	Name             string `json:"name"`
	Password         string `json:"password"`
	OrganizationName string `json:"organization_name"`
}

// normalize trims/lower-cases fields in place and returns field errors.
func (in *RegisterInput) normalize() map[string]string {
	errs := map[string]string{}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Name = strings.TrimSpace(in.Name)
	in.OrganizationName = strings.TrimSpace(in.OrganizationName)

	if addr, err := mail.ParseAddress(in.Email); err != nil || addr.Address != in.Email || len(in.Email) > 254 {
		errs["email"] = "must be a valid email address"
	}
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 {
		errs["name"] = "must be 1-100 characters"
	}
	if n := utf8.RuneCountInString(in.Password); n < minPasswordLen || n > maxPasswordLen {
		errs["password"] = fmt.Sprintf("must be %d-%d characters", minPasswordLen, maxPasswordLen)
	}
	if n := utf8.RuneCountInString(in.OrganizationName); n < 1 || n > 100 {
		errs["organization_name"] = "must be 1-100 characters"
	}
	return errs
}

// Register creates a user and a new organization with the user as admin,
// atomically, then logs the user in.
func (s *Service) Register(ctx context.Context, in RegisterInput) (User, TokenPair, error) {
	hash, err := HashPassword(in.Password)
	if err != nil {
		return User{}, TokenPair{}, err
	}
	u, err := s.store.createUserWithOrg(ctx, in.Email, in.Name, hash, in.OrganizationName, slug.WithSuffix(in.OrganizationName))
	if err != nil {
		return User{}, TokenPair{}, err
	}
	pair, err := s.issuePair(ctx, u.ID)
	return u, pair, err
}

// Login verifies credentials and returns a fresh token pair (new family).
func (s *Service) Login(ctx context.Context, email, password string) (User, TokenPair, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(password) > maxPasswordLen*4 { // bytes; bound argon2 work
		return User{}, TokenPair{}, ErrInvalidCredentials
	}
	u, hash, err := s.store.userByEmail(ctx, email)
	if errors.Is(err, errNotFound) {
		_, _ = VerifyPassword(password, s.dummyHash) // equalize timing
		return User{}, TokenPair{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, TokenPair{}, err
	}
	ok, err := VerifyPassword(password, hash)
	if err != nil {
		return User{}, TokenPair{}, fmt.Errorf("verify password for user %s: %w", u.ID, err)
	}
	if !ok {
		return User{}, TokenPair{}, ErrInvalidCredentials
	}
	// Logins are platform-level events (no organization).
	if err := audit.Record(ctx, s.store.pool, audit.Actor{UserID: u.ID}, "auth.login", "user", u.ID, nil); err != nil {
		return User{}, TokenPair{}, err
	}
	pair, err := s.issuePair(ctx, u.ID)
	return u, pair, err
}

// Refresh rotates a refresh token: the presented token is revoked and a new
// one in the same family is issued. Presenting an already-revoked token
// revokes the whole family (theft detection).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	newToken, newHash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	expires := time.Now().Add(s.refreshTTL)
	userID, err := s.store.rotateRefreshToken(ctx, hashRefreshToken(refreshToken), newHash, expires)
	if err != nil {
		return TokenPair{}, err
	}
	access, accessExp, err := s.tokens.Issue(userID)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, AccessExpiresAt: accessExp, RefreshToken: newToken, RefreshExpiresAt: expires, TokenType: "Bearer"}, nil
}

// Logout revokes the refresh token's family. Unknown tokens are ignored so
// logout is idempotent and reveals nothing.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	return s.store.revokeFamilyOf(ctx, hashRefreshToken(refreshToken))
}

// Me returns the user and their organization memberships.
func (s *Service) Me(ctx context.Context, userID string) (User, []Membership, error) {
	u, err := s.store.userByID(ctx, userID)
	if err != nil {
		return User{}, nil, err
	}
	ms, err := s.store.memberships(ctx, userID)
	return u, ms, err
}

// Authorize returns nil if the user's role in orgID allows need, or
// ErrForbidden otherwise (including when the user is not a member, so
// callers cannot probe which organizations exist).
func (s *Service) Authorize(ctx context.Context, userID, orgID string, need Role) error {
	role, err := s.store.roleIn(ctx, userID, orgID)
	if errors.Is(err, errNotFound) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if !role.Allows(need) {
		return ErrForbidden
	}
	return nil
}

// VerifyAccessToken validates an access token and returns the user ID.
func (s *Service) VerifyAccessToken(token string) (string, error) {
	return s.tokens.Verify(token)
}

// issuePair starts a new token family (one per login session).
func (s *Service) issuePair(ctx context.Context, userID string) (TokenPair, error) {
	access, accessExp, err := s.tokens.Issue(userID)
	if err != nil {
		return TokenPair{}, err
	}
	refresh, refreshHash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	expires := time.Now().Add(s.refreshTTL)
	if err := s.store.insertFamilyRoot(ctx, userID, refreshHash, expires); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, AccessExpiresAt: accessExp, RefreshToken: refresh, RefreshExpiresAt: expires, TokenType: "Bearer"}, nil
}
