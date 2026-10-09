//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/database/dbtest"
	"github.com/vipul2902/clouddeploy/internal/httpapi"
)

type env struct {
	srv  *httptest.Server
	svc  *auth.Service
	pool *pgxpool.Pool
}

func setup(t *testing.T) env {
	t.Helper()
	pool := dbtest.New(t)
	tokens, err := auth.NewTokenIssuer([]byte("integration-test-secret-32-bytes!!"), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(pool, tokens, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Auth:   auth.NewHandler(svc),
	}))
	t.Cleanup(srv.Close)
	return env{srv: srv, svc: svc, pool: pool}
}

func (e env) post(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(e.srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, resp)
}

func (e env) get(t *testing.T, path, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, resp)
}

func decode(t *testing.T, resp *http.Response) (int, map[string]any) {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	if resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatalf("decode %d response: %v", resp.StatusCode, err)
		}
	}
	return resp.StatusCode, m
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

const password = "a-strong-test-password"

func register(t *testing.T, e env, email string) map[string]any {
	t.Helper()
	code, body := e.post(t, "/api/v1/auth/register", map[string]string{
		"email": email, "name": "Test User", "password": password, "organization_name": "Test Org",
	})
	if code != http.StatusCreated {
		t.Fatalf("register: %d %v", code, body)
	}
	return body
}

func TestRegisterLoginMe(t *testing.T) {
	e := setup(t)
	reg := register(t, e, "Alice@Example.com")

	if reg["refresh_token"] == "" || reg["access_token"] == "" {
		t.Fatalf("missing tokens: %v", reg)
	}
	if _, leaked := reg["user"].(map[string]any)["password_hash"]; leaked {
		t.Fatal("password hash leaked in response")
	}

	// Duplicate email (case-insensitive) is rejected.
	code, body := e.post(t, "/api/v1/auth/register", map[string]string{
		"email": "alice@example.com", "name": "A", "password": password, "organization_name": "Other",
	})
	if code != http.StatusConflict || errCode(body) != "EMAIL_TAKEN" {
		t.Fatalf("duplicate register: %d %v", code, body)
	}

	code, login := e.post(t, "/api/v1/auth/login", map[string]string{"email": "ALICE@example.com", "password": password})
	if code != http.StatusOK {
		t.Fatalf("login: %d %v", code, login)
	}

	code, me := e.get(t, "/api/v1/auth/me", login["access_token"].(string))
	if code != http.StatusOK {
		t.Fatalf("me: %d %v", code, me)
	}
	ms := me["memberships"].([]any)
	if len(ms) != 1 || ms[0].(map[string]any)["role"] != "admin" {
		t.Errorf("registering user should be admin of new org: %v", ms)
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	e := setup(t)
	register(t, e, "bob@example.com")

	c1, b1 := e.post(t, "/api/v1/auth/login", map[string]string{"email": "bob@example.com", "password": "wrong-password-123"})
	c2, b2 := e.post(t, "/api/v1/auth/login", map[string]string{"email": "nobody@example.com", "password": "wrong-password-123"})
	if c1 != http.StatusUnauthorized || c2 != http.StatusUnauthorized {
		t.Fatalf("statuses = %d, %d; want 401, 401", c1, c2)
	}
	if errCode(b1) != errCode(b2) || b1["error"].(map[string]any)["message"] != b2["error"].(map[string]any)["message"] {
		t.Errorf("responses differ, enabling user enumeration: %v vs %v", b1, b2)
	}
}

func TestRegisterValidation(t *testing.T) {
	e := setup(t)
	code, body := e.post(t, "/api/v1/auth/register", map[string]string{"email": "x", "name": "", "password": "short", "organization_name": ""})
	if code != http.StatusUnprocessableEntity || errCode(body) != "VALIDATION_FAILED" {
		t.Fatalf("got %d %v", code, body)
	}
	if d := body["error"].(map[string]any)["details"].(map[string]any); len(d) != 4 {
		t.Errorf("want 4 field errors, got %v", d)
	}
	code, body = e.post(t, "/api/v1/auth/register", map[string]any{"email": "a@b.co", "is_admin": true})
	if code != http.StatusBadRequest {
		t.Errorf("unknown field accepted: %d %v", code, body)
	}
}

func TestMeRequiresValidToken(t *testing.T) {
	e := setup(t)
	for name, tok := range map[string]string{"missing": "", "garbage": "not-a-jwt"} {
		if code, _ := e.get(t, "/api/v1/auth/me", tok); code != http.StatusUnauthorized {
			t.Errorf("%s token: status %d, want 401", name, code)
		}
	}
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	e := setup(t)
	r0 := register(t, e, "carol@example.com")["refresh_token"].(string)

	// Rotate: r0 -> r1.
	code, body := e.post(t, "/api/v1/auth/refresh", map[string]string{"refresh_token": r0})
	if code != http.StatusOK {
		t.Fatalf("refresh: %d %v", code, body)
	}
	r1 := body["refresh_token"].(string)
	if r1 == r0 {
		t.Fatal("refresh token was not rotated")
	}

	// Attacker replays the stolen r0 -> rejected AND the family is revoked...
	if code, _ := e.post(t, "/api/v1/auth/refresh", map[string]string{"refresh_token": r0}); code != http.StatusUnauthorized {
		t.Fatalf("reused token: status %d, want 401", code)
	}
	// ...so the legitimate holder's r1 no longer works either.
	if code, _ := e.post(t, "/api/v1/auth/refresh", map[string]string{"refresh_token": r1}); code != http.StatusUnauthorized {
		t.Fatalf("family not revoked after reuse: status %d", code)
	}

	// A separate login session (different family) is unaffected.
	_, login := e.post(t, "/api/v1/auth/login", map[string]string{"email": "carol@example.com", "password": password})
	if code, _ := e.post(t, "/api/v1/auth/refresh", map[string]string{"refresh_token": login["refresh_token"].(string)}); code != http.StatusOK {
		t.Fatalf("other session affected: status %d", code)
	}
}

func TestConcurrentRefreshOnlyOneSucceeds(t *testing.T) {
	e := setup(t)
	r0 := register(t, e, "dave@example.com")["refresh_token"].(string)

	const n = 5
	var wg sync.WaitGroup
	results := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Refresh(context.Background(), r0)
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	ok := 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, auth.ErrRefreshReuse), errors.Is(err, auth.ErrInvalidRefresh):
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent refreshes succeeded, want exactly 1", ok)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	e := setup(t)
	r0 := register(t, e, "erin@example.com")["refresh_token"].(string)

	if code, _ := e.post(t, "/api/v1/auth/logout", map[string]string{"refresh_token": r0}); code != http.StatusNoContent {
		t.Fatalf("logout: status %d", code)
	}
	if code, _ := e.post(t, "/api/v1/auth/refresh", map[string]string{"refresh_token": r0}); code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: status %d, want 401", code)
	}
	// Logout is idempotent, also for unknown tokens.
	if code, _ := e.post(t, "/api/v1/auth/logout", map[string]string{"refresh_token": "unknown"}); code != http.StatusNoContent {
		t.Fatalf("logout unknown: status %d", code)
	}
}

func TestAuthorize(t *testing.T) {
	e := setup(t)
	reg := register(t, e, "frank@example.com")
	userID := reg["user"].(map[string]any)["id"].(string)

	_, me := e.get(t, "/api/v1/auth/me", reg["access_token"].(string))
	orgID := me["memberships"].([]any)[0].(map[string]any)["organization_id"].(string)
	ctx := context.Background()

	if err := e.svc.Authorize(ctx, userID, orgID, auth.RoleAdmin); err != nil {
		t.Errorf("admin denied in own org: %v", err)
	}
	// Non-member: forbidden, not "not found" (no org existence probing).
	other := register(t, e, "grace@example.com")["user"].(map[string]any)["id"].(string)
	if err := e.svc.Authorize(ctx, other, orgID, auth.RoleViewer); !errors.Is(err, auth.ErrForbidden) {
		t.Errorf("non-member: err = %v, want ErrForbidden", err)
	}

	// Add grace as a viewer: may read, may not deploy.
	if _, err := e.pool.Exec(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'viewer')`, orgID, other); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Authorize(ctx, other, orgID, auth.RoleViewer); err != nil {
		t.Errorf("viewer denied read: %v", err)
	}
	if err := e.svc.Authorize(ctx, other, orgID, auth.RoleDeveloper); !errors.Is(err, auth.ErrForbidden) {
		t.Errorf("viewer allowed developer action: err = %v", err)
	}
}
