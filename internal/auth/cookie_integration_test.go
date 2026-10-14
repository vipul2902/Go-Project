//go:build integration

package auth_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// browserCall sends a request the way the dashboard does, optionally with
// the refresh cookie and the X-Auth-Mode header.
// browserResp is what the tests inspect: the body is read and closed inside
// browserCall, so no caller holds an open response.
type browserResp struct {
	StatusCode int
	cookies    []*http.Cookie
}

func (r browserResp) Cookies() []*http.Cookie { return r.cookies }

func browserCall(t *testing.T, e env, path string, body any, cookie *http.Cookie, cookieHeader bool) (browserResp, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookieHeader {
		req.Header.Set("X-Auth-Mode", "cookie")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return browserResp{StatusCode: resp.StatusCode, cookies: resp.Cookies()}, m
}

func refreshCookieOf(resp browserResp) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "cd_refresh" {
			return c
		}
	}
	return nil
}

func TestCookieModeKeepsRefreshTokenAwayFromJavaScript(t *testing.T) {
	e := setup(t)
	register(t, e, "browser@example.com")

	resp, body := browserCall(t, e, "/api/v1/auth/login", map[string]string{"email": "browser@example.com", "password": password}, nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %v", resp.StatusCode, body)
	}
	c := refreshCookieOf(resp)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api/v1/auth" {
		t.Fatalf("cookie attributes: %+v", c)
	}
	if _, present := body["refresh_token"]; present {
		t.Error("refresh token returned in the JSON body in cookie mode")
	}
	if body["access_token"] == "" {
		t.Error("no access token")
	}

	// CSRF defense: the cookie alone (as a cross-site form would send it) is
	// not enough; the custom header is required.
	if resp, _ := browserCall(t, e, "/api/v1/auth/refresh", nil, c, false); resp.StatusCode == http.StatusOK {
		t.Fatal("refresh succeeded with the cookie but without X-Auth-Mode")
	}

	// Refresh rotates the cookie.
	resp, body = browserCall(t, e, "/api/v1/auth/refresh", nil, c, true)
	c2 := refreshCookieOf(resp)
	if resp.StatusCode != http.StatusOK || c2 == nil || c2.Value == c.Value || body["access_token"] == "" {
		t.Fatalf("refresh: %d rotated=%v %v", resp.StatusCode, c2 != nil && c2.Value != c.Value, body)
	}
	// The old cookie value is now a reused token: rejected.
	if resp, _ := browserCall(t, e, "/api/v1/auth/refresh", nil, c, true); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("reused cookie: %d", resp.StatusCode)
	}

	// Logout clears the cookie and revokes the session.
	e2 := e
	login, _ := browserCall(t, e2, "/api/v1/auth/login", map[string]string{"email": "browser@example.com", "password": password}, nil, true)
	c3 := refreshCookieOf(login)
	resp, _ = browserCall(t, e, "/api/v1/auth/logout", nil, c3, true)
	cleared := refreshCookieOf(resp)
	if resp.StatusCode != http.StatusNoContent || cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("logout: %d cleared=%+v", resp.StatusCode, cleared)
	}
	if resp, _ := browserCall(t, e, "/api/v1/auth/refresh", nil, c3, true); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("refresh after logout: %d", resp.StatusCode)
	}
	// No cookie at all in cookie mode: 401, not a body-parsing error.
	if resp, _ := browserCall(t, e, "/api/v1/auth/refresh", nil, nil, true); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing cookie: %d", resp.StatusCode)
	}
}
