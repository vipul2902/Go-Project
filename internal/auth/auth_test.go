package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestTokenRoundTrip(t *testing.T) {
	ti, err := NewTokenIssuer(testSecret, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tok, exp, err := ti.Issue("user-1")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < 14*time.Minute || d > 15*time.Minute {
		t.Errorf("unexpected expiry in %v", d)
	}
	sub, err := ti.Verify(tok)
	if err != nil || sub != "user-1" {
		t.Fatalf("Verify = %q, %v", sub, err)
	}
}

func TestNewTokenIssuerRejectsShortSecret(t *testing.T) {
	if _, err := NewTokenIssuer([]byte("short"), time.Minute); err == nil {
		t.Fatal("expected error for short secret")
	}
}

func TestVerifyRejects(t *testing.T) {
	ti, _ := NewTokenIssuer(testSecret, 15*time.Minute)
	valid, _, _ := ti.Issue("user-1")

	expired := func() string {
		old, _ := NewTokenIssuer(testSecret, time.Minute)
		old.now = func() time.Time { return time.Now().Add(-time.Hour) }
		tok, _, _ := old.Issue("user-1")
		return tok
	}()
	otherKey := func() string {
		o, _ := NewTokenIssuer([]byte("ffffffffffffffffffffffffffffffff"), time.Minute)
		tok, _, _ := o.Issue("user-1")
		return tok
	}()
	algNone := func() string {
		tok, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{
			Issuer: issuer, Subject: "user-1", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}).SignedString(jwt.UnsafeAllowNoneSignatureType)
		return tok
	}()
	wrongIssuer := func() string {
		tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
			Issuer: "evil", Subject: "user-1", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}).SignedString(testSecret)
		return tok
	}()
	noExpiry := func() string {
		tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Issuer: issuer, Subject: "user-1"}).SignedString(testSecret)
		return tok
	}()
	parts := strings.Split(valid, ".")
	tampered := parts[0] + "." + parts[1] + "x." + parts[2]

	for name, tok := range map[string]string{
		"expired": expired, "other key": otherKey, "alg none": algNone,
		"wrong issuer": wrongIssuer, "no expiry": noExpiry, "tampered": tampered, "garbage": "abc",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ti.Verify(tok); err != ErrInvalidToken {
				t.Errorf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestRefreshTokenUniqueAndHashed(t *testing.T) {
	a, ha, _ := newRefreshToken()
	b, _, _ := newRefreshToken()
	if a == b {
		t.Fatal("refresh tokens collided")
	}
	if len(a) != 43 || len(ha) != 32 {
		t.Errorf("unexpected lengths: token=%d hash=%d", len(a), len(ha))
	}
	if string(hashRefreshToken(a)) != string(ha) {
		t.Error("hash is not deterministic")
	}
}

func TestRoleAllows(t *testing.T) {
	tests := []struct {
		have, need Role
		want       bool
	}{
		{RoleAdmin, RoleAdmin, true},
		{RoleAdmin, RoleViewer, true},
		{RoleDeveloper, RoleDeveloper, true},
		{RoleDeveloper, RoleAdmin, false},
		{RoleViewer, RoleViewer, true},
		{RoleViewer, RoleDeveloper, false},
		{Role("owner"), RoleViewer, false}, // unknown role: fail closed
		{RoleAdmin, Role("superuser"), false},
		{Role(""), RoleViewer, false},
	}
	for _, tt := range tests {
		if got := tt.have.Allows(tt.need); got != tt.want {
			t.Errorf("%q.Allows(%q) = %v, want %v", tt.have, tt.need, got, tt.want)
		}
	}
}

func TestValidateRegister(t *testing.T) {
	ok := RegisterInput{Email: " Alice@Example.COM ", Name: "Alice", Password: "a-long-password!", OrganizationName: "Acme Inc"}
	if d := ok.normalize(); len(d) != 0 {
		t.Fatalf("valid input rejected: %v", d)
	}
	if ok.Email != "alice@example.com" {
		t.Errorf("email not normalized: %q", ok.Email)
	}

	bad := RegisterInput{Email: "nope", Name: "", Password: "short", OrganizationName: strings.Repeat("x", 101)}
	d := bad.normalize()
	for _, f := range []string{"email", "name", "password", "organization_name"} {
		if _, ok := d[f]; !ok {
			t.Errorf("missing validation error for %s: %v", f, d)
		}
	}
	long := RegisterInput{Email: "a@b.co", Name: "A", Password: strings.Repeat("p", 129), OrganizationName: "Org"}
	if _, ok := long.normalize()["password"]; !ok {
		t.Error("over-long password accepted (argon2 DoS risk)")
	}
}

func TestDeclaredRateLimitRulesAreValid(t *testing.T) {
	for _, r := range []interface{ Validate() error }{LoginPerIP, LoginPerAccount, RegisterPerIP, RefreshPerIP, APIPerUser} {
		if err := r.Validate(); err != nil {
			t.Error(err)
		}
	}
}
