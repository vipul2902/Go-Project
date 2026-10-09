package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const issuer = "clouddeploy"

// ErrInvalidToken is returned for any access token that fails validation.
// Callers get no detail about *why*, by design.
var ErrInvalidToken = errors.New("invalid access token")

// TokenIssuer signs and verifies access tokens (JWT, HS256).
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewTokenIssuer returns an issuer. The secret must be at least 32 bytes:
// HS256 is only as strong as its key.
func NewTokenIssuer(secret []byte, ttl time.Duration) (*TokenIssuer, error) {
	if len(secret) < 32 {
		return nil, errors.New("JWT secret must be at least 32 bytes")
	}
	return &TokenIssuer{secret: secret, ttl: ttl, now: time.Now}, nil
}

// Issue returns a signed access token for userID and its expiry.
func (t *TokenIssuer) Issue(userID string) (string, time.Time, error) {
	now := t.now()
	exp := now.Add(t.ttl)
	claims := jwt.RegisteredClaims{
		Issuer:    issuer,
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, exp, nil
}

// Verify validates signature, algorithm, issuer and expiry and returns the
// user ID. Pinning the algorithm blocks "alg: none" and key-confusion attacks.
func (t *TokenIssuer) Verify(token string) (string, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil || claims.Subject == "" {
		return "", ErrInvalidToken
	}
	return claims.Subject, nil
}

// newRefreshToken returns a random opaque token and the SHA-256 hash that is
// stored. 32 random bytes make guessing infeasible, so a fast hash (not
// argon2) is appropriate here, unlike for human-chosen passwords.
func newRefreshToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("generate refresh token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashRefreshToken(token), nil
}

func hashRefreshToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
