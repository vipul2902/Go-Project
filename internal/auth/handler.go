package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vipul2902/clouddeploy/internal/httpx"
	"github.com/vipul2902/clouddeploy/internal/middleware"
	"github.com/vipul2902/clouddeploy/internal/ratelimit"
)

// Handler exposes auth endpoints over HTTP.
type Handler struct {
	svc     *Service
	limiter *ratelimit.Limiter
}

// NewHandler returns the auth HTTP handler. A nil limiter disables limits.
func NewHandler(svc *Service, limiter *ratelimit.Limiter) *Handler {
	return &Handler{svc: svc, limiter: limiter}
}

// Routes registers the auth endpoints on mux.
func (h *Handler) Routes(mux httpx.Router) {
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/refresh", h.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.Handle("GET /api/v1/auth/me", h.RequireAuth(http.HandlerFunc(h.me)))
}

type authResponse struct {
	User User `json:"user"`
	TokenPair
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if !h.limiter.Check(w, r, RegisterPerIP, ipKey(r), httpx.TooManyRequests) {
		return
	}
	var in RegisterInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if errs := in.normalize(); len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}
	u, pair, err := h.svc.Register(r.Context(), in)
	switch {
	case errors.Is(err, ErrEmailTaken):
		httpx.WriteError(w, r, http.StatusConflict, "EMAIL_TAKEN", "an account with this email already exists")
	case err != nil:
		httpx.WriteInternalError(w, r, err)
	default:
		noStore(w)
		httpx.WriteJSON(w, http.StatusCreated, authResponse{User: u, TokenPair: pair})
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if !h.limiter.Check(w, r, LoginPerIP, ipKey(r), httpx.TooManyRequests) ||
		!h.limiter.Check(w, r, LoginPerAccount, accountKey(in.Email), httpx.TooManyRequests) {
		return
	}
	u, pair, err := h.svc.Login(r.Context(), in.Email, in.Password)
	if err == nil {
		// Count failures, not logins: a successful login clears the account limit.
		if rerr := h.limiter.Reset(r.Context(), LoginPerAccount, accountKey(in.Email)); rerr != nil {
			logWarn(r, "reset login rate limit failed")
		}
	}
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		// Same response for unknown email and wrong password.
		httpx.WriteError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid email or password")
	case err != nil:
		httpx.WriteInternalError(w, r, err)
	default:
		h.writeTokens(w, r, http.StatusOK, u, pair)
	}
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	if !h.limiter.Check(w, r, RefreshPerIP, ipKey(r), httpx.TooManyRequests) {
		return
	}
	token, ok := refreshTokenFrom(w, r)
	if !ok {
		return
	}
	pair, err := h.svc.Refresh(r.Context(), token)
	switch {
	case errors.Is(err, ErrInvalidRefresh), errors.Is(err, ErrRefreshReuse):
		// Both look identical to the client; reuse is logged server-side.
		if errors.Is(err, ErrRefreshReuse) {
			logWarn(r, "refresh token reuse detected; token family revoked")
		}
		httpx.WriteError(w, r, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "refresh token is invalid or expired")
	case err != nil:
		httpx.WriteInternalError(w, r, err)
	default:
		h.writeTokens(w, r, http.StatusOK, User{}, pair)
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	token, ok := refreshTokenFrom(w, r)
	if !ok {
		return
	}
	if err := h.svc.Logout(r.Context(), token); err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	if cookieMode(r) {
		http.SetCookie(w, &http.Cookie{Name: refreshCookie, Path: refreshCookiePath, MaxAge: -1,
			HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	}
	w.WriteHeader(http.StatusNoContent)
}

// Browser token handling ("cookie mode", opted into with X-Auth-Mode: cookie):
// the refresh token travels only in an HttpOnly cookie that JavaScript can
// never read (so XSS cannot steal it), scoped to the auth endpoints, and
// SameSite=Strict. Requiring the custom header for cookie-based refresh and
// logout is the CSRF defense: cross-site forms cannot send custom headers,
// and the API never enables CORS. Non-browser clients keep the JSON flow.
const (
	HeaderAuthMode    = "X-Auth-Mode"
	refreshCookie     = "cd_refresh"
	refreshCookiePath = "/api/v1/auth"
)

func cookieMode(r *http.Request) bool { return r.Header.Get(HeaderAuthMode) == "cookie" }

// refreshTokenFrom reads the refresh token from the cookie (cookie mode) or
// the JSON body, writing an error response and returning false if absent.
func refreshTokenFrom(w http.ResponseWriter, r *http.Request) (string, bool) {
	if cookieMode(r) {
		c, err := r.Cookie(refreshCookie)
		if err != nil || c.Value == "" {
			httpx.WriteError(w, r, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "refresh token is invalid or expired")
			return "", false
		}
		return c.Value, true
	}
	var in refreshRequest
	if !httpx.DecodeJSON(w, r, &in) {
		return "", false
	}
	return in.RefreshToken, true
}

// writeTokens returns a token pair. In cookie mode the refresh token is set
// as a cookie and omitted from the body.
func (h *Handler) writeTokens(w http.ResponseWriter, r *http.Request, status int, u User, pair TokenPair) {
	noStore(w)
	if cookieMode(r) {
		http.SetCookie(w, &http.Cookie{
			Name: refreshCookie, Value: pair.RefreshToken, Path: refreshCookiePath,
			Expires: pair.RefreshExpiresAt, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
		})
		pair.RefreshToken = ""
	}
	if u.ID == "" {
		httpx.WriteJSON(w, status, pair)
		return
	}
	httpx.WriteJSON(w, status, authResponse{User: u, TokenPair: pair})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, ms, err := h.svc.Me(r.Context(), UserIDFrom(r.Context()))
	if errors.Is(err, errNotFound) {
		// Valid token for a deleted user.
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, "authentication required")
		return
	}
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		User        User         `json:"user"`
		Memberships []Membership `json:"memberships"`
	}{u, ms})
}

type userCtxKey struct{}

// UserIDFrom returns the authenticated user ID set by RequireAuth.
func UserIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(userCtxKey{}).(string)
	return id
}

// RequireAuth rejects requests without a valid "Authorization: Bearer" access
// token and stores the user ID in the request context.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="clouddeploy"`)
			httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, "authentication required")
			return
		}
		userID, err := h.svc.VerifyAccessToken(token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="clouddeploy", error="invalid_token"`)
			httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, "invalid or expired access token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, userID)))
	})
}

// noStore prevents browsers and proxies from caching token responses.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func logWarn(r *http.Request, msg string) {
	slog.WarnContext(r.Context(), msg, "request_id", middleware.RequestIDFrom(r.Context()), "path", r.URL.Path)
}
