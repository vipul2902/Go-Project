package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// publicRoutes are the only routes that may answer without authentication.
// Adding a route to this list is a security decision: review it.
var publicRoutes = map[string]bool{
	"GET /healthz":               true,
	"GET /readyz":                true,
	"POST /api/v1/auth/register": true,
	"POST /api/v1/auth/login":    true,
	"POST /api/v1/auth/refresh":  true,
	"POST /api/v1/auth/logout":   true,
	"/":                          true, // catch-all 404
}

var wildcard = regexp.MustCompile(`\{[^}]+\}`)

// Every route of the production wiring rejects unauthenticated and
// invalid-token requests, except the explicit allowlist above. A new route
// registered without RequireAuth makes this test fail.
func TestNoRouteIsAccidentallyPublic(t *testing.T) {
	cfg := AppConfig{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), JWTSecret: []byte("routes-test-secret-at-least-32-bytes"),
		AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour,
	}
	routes, err := AppRoutes(nil, cfg) // no database: requests are rejected before any query
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewApp(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(routes)
	if len(routes) < 35 {
		t.Fatalf("only %d routes recorded; recording is broken? %v", len(routes), routes)
	}

	checked := 0
	for _, route := range routes {
		if publicRoutes[route] {
			continue
		}
		method, path, ok := strings.Cut(route, " ")
		if !ok {
			t.Errorf("route %q has no method: every API route must be method-specific", route)
			continue
		}
		path = wildcard.ReplaceAllString(path, "00000000-0000-4000-8000-000000000001")
		path = strings.ReplaceAll(path, "{env}", "development")
		for _, auth := range []string{"", "Bearer not-a-jwt", "Basic dXNlcjpwYXNz"} {
			req := httptest.NewRequest(method, path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"code":"UNAUTHORIZED"`) {
				t.Errorf("%s (auth %q): status %d body %s", route, auth, rec.Code, rec.Body.String())
			}
		}
		checked++
	}
	t.Logf("%d protected routes reject unauthenticated requests; %d public by design", checked, len(routes)-checked)
}
