// Package apitest runs the real application (production wiring from
// httpapi.NewApp) against the test database, for HTTP-level integration
// tests. Tests using it must run with -p 1 (shared test database).
package apitest

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vipul2902/clouddeploy/internal/database/dbtest"
	"github.com/vipul2902/clouddeploy/internal/httpapi"
)

// API is a running test server.
type API struct {
	t    testing.TB
	URL  string
	Pool *pgxpool.Pool
}

// New starts the application on an httptest server with a clean database.
func New(t testing.TB) *API {
	t.Helper()
	pool := dbtest.New(t)
	h, err := httpapi.NewApp(pool, httpapi.AppConfig{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:         "test",
		JWTSecret:       []byte("apitest-secret-at-least-32-bytes!!"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &API{t: t, URL: srv.URL, Pool: pool}
}

// User is a registered test user with their own new organization.
type User struct {
	ID    string
	Email string
	Token string
	OrgID string // the organization created at registration (user is admin)
}

// Register creates a user (and their organization) and returns credentials.
func (a *API) Register(email string) User {
	a.t.Helper()
	res := a.Do(http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": email, "name": "Test User", "password": "a-strong-test-password", "organization_name": "Org of " + email,
	})
	if res.Status != http.StatusCreated {
		a.t.Fatalf("register %s: %d %v", email, res.Status, res.Body)
	}
	u := User{Email: email, ID: res.Str("user", "id"), Token: res.Str("access_token")}
	me := a.Do(http.MethodGet, "/api/v1/auth/me", u.Token, nil)
	u.OrgID = me.Body["memberships"].([]any)[0].(map[string]any)["organization_id"].(string)
	return u
}

// AddMember inserts a membership directly (test setup shortcut).
func (a *API) AddMember(orgID, userID, role string) {
	a.t.Helper()
	if _, err := a.Pool.Exec(a.t.Context(),
		`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)`, orgID, userID, role); err != nil {
		a.t.Fatal(err)
	}
}

// Response is a decoded HTTP response.
type Response struct {
	Status int
	Header http.Header
	Body   map[string]any
}

// Do sends a JSON request. token may be empty; headers are optional pairs.
func (a *API) Do(method, path, token string, body any, headers ...string) Response {
	a.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.URL+path, r)
	if err != nil {
		a.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := Response{Status: resp.StatusCode, Header: resp.Header}
	if raw, _ := io.ReadAll(resp.Body); len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.Body); err != nil {
			a.t.Fatalf("%s %s: decode %d response %q: %v", method, path, resp.StatusCode, raw, err)
		}
	}
	return out
}

// Str walks nested objects by key and returns the string at the end ("" if absent).
func (r Response) Str(keys ...string) string {
	var cur any = r.Body
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	s, _ := cur.(string)
	return s
}

// ErrCode returns error.code from an error envelope.
func (r Response) ErrCode() string { return r.Str("error", "code") }

// Data returns the "data" array of a list response.
func (r Response) Data() []map[string]any {
	items, _ := r.Body["data"].([]any)
	out := make([]map[string]any, len(items))
	for i, it := range items {
		out[i], _ = it.(map[string]any)
	}
	return out
}
