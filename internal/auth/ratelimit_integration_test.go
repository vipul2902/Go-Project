//go:build integration

package auth_test

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/httpapi/apitest"
)

func TestLoginBruteForceIsLimitedPerAccount(t *testing.T) {
	api := apitest.New(t, apitest.WithRedis())
	api.Register("victim@example.com")
	login := func(pw string) apitest.Response {
		return api.Do(http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": "victim@example.com", "password": pw})
	}

	for i := range 10 {
		if res := login(fmt.Sprintf("guess-%02d-wrong", i)); res.Status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, res.Status)
		}
	}
	res := login("guess-11-wrong")
	if res.Status != http.StatusTooManyRequests || res.ErrCode() != "RATE_LIMITED" {
		t.Fatalf("11th attempt: %d %v", res.Status, res.Body)
	}
	if ra, _ := strconv.Atoi(res.Header.Get("Retry-After")); ra < 1 {
		t.Errorf("Retry-After = %q", res.Header.Get("Retry-After"))
	}
	// Even the correct password is refused while limited: the limiter runs
	// before the password check, so it cannot be used as an oracle.
	if res := login("a-strong-test-password"); res.Status != http.StatusTooManyRequests {
		t.Errorf("correct password during lockout: %d", res.Status)
	}
	// Other accounts are unaffected.
	api.Register("bystander@example.com")
	if res := api.Do(http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": "bystander@example.com", "password": "a-strong-test-password"}); res.Status != http.StatusOK {
		t.Errorf("other account: %d", res.Status)
	}
}

func TestSuccessfulLoginResetsAccountLimit(t *testing.T) {
	api := apitest.New(t, apitest.WithRedis())
	api.Register("user@example.com")
	login := func(pw string) int {
		return api.Do(http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": "user@example.com", "password": pw}).Status
	}
	for range 9 {
		login("wrong-password-xx")
	}
	if s := login("a-strong-test-password"); s != http.StatusOK {
		t.Fatalf("login: %d", s)
	}
	// Counter reset: 9 more failures are still answered 401, not 429.
	for i := range 9 {
		if s := login("wrong-password-xx"); s != http.StatusUnauthorized {
			t.Fatalf("failure %d after reset: %d", i+1, s)
		}
	}
}

func TestRegistrationLimitedPerIP(t *testing.T) {
	api := apitest.New(t, apitest.WithRedis())
	statuses := make([]int, 6)
	for i := range statuses {
		statuses[i] = api.Do(http.MethodPost, "/api/v1/auth/register", "", map[string]string{
			"email": fmt.Sprintf("u%d@example.com", i), "name": "U", "password": "a-strong-test-password", "organization_name": "O",
		}).Status
	}
	if fmt.Sprint(statuses) != "[201 201 201 201 201 429]" {
		t.Errorf("statuses = %v", statuses)
	}
}

func TestAuthenticatedRequestsCarryRateLimitHeaders(t *testing.T) {
	api := apitest.New(t, apitest.WithRedis())
	u := api.Register("alice@example.com")
	res := api.Do(http.MethodGet, "/api/v1/projects", u.Token, nil)
	if res.Header.Get("RateLimit-Limit") != "600" || res.Header.Get("RateLimit-Remaining") != "599" {
		t.Errorf("headers: limit=%q remaining=%q", res.Header.Get("RateLimit-Limit"), res.Header.Get("RateLimit-Remaining"))
	}
	// Unauthenticated requests are rejected before consuming the user budget.
	if res := api.Do(http.MethodGet, "/api/v1/projects", "", nil); res.Header.Get("RateLimit-Limit") != "" {
		t.Error("unauthenticated request consumed a per-user limit")
	}
}
