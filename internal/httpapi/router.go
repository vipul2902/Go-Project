package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/environments"
	"github.com/vipul2902/clouddeploy/internal/httpx"
	"github.com/vipul2902/clouddeploy/internal/middleware"
	"github.com/vipul2902/clouddeploy/internal/organizations"
	"github.com/vipul2902/clouddeploy/internal/projects"
	"github.com/vipul2902/clouddeploy/internal/ratelimit"
)

// ReadinessCheck reports whether a dependency (database, cache, ...) is usable.
type ReadinessCheck func(ctx context.Context) error

// Deps are the dependencies the HTTP layer needs. Later phases add services here.
type Deps struct {
	Logger  *slog.Logger
	Version string
	// Checks are run by /readyz; keyed by dependency name. A failing Check
	// makes the instance unready (503). A failing SoftCheck only reports
	// "degraded" (200): the dependency is optional, e.g. Redis, whose loss
	// disables rate limiting (fail open) but not the API.
	Checks     map[string]ReadinessCheck
	SoftChecks map[string]ReadinessCheck
	// Limiter applies the per-user API limit after authentication (nil: off).
	Limiter *ratelimit.Limiter
	// Handlers are optional so health endpoints can be tested in isolation.
	Auth          *auth.Handler
	Organizations *organizations.Handler
	Projects      *projects.Handler
	Environments  *environments.Handler
	Deployments   *deployments.Handler
}

// NewRouter builds the root handler with all routes and middleware.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handleLiveness(d.Version))
	mux.HandleFunc("GET /readyz", handleReadiness(d.Logger, d.Checks, d.SoftChecks))
	if d.Auth != nil {
		d.Auth.Routes(mux)
		// Authenticate first (sets the user), then rate-limit per user.
		perUser := d.Limiter.Middleware(auth.APIPerUser, auth.UserKey, httpx.TooManyRequests)
		protected := func(h http.Handler) http.Handler { return d.Auth.RequireAuth(perUser(h)) }
		if d.Organizations != nil {
			d.Organizations.Routes(mux, protected)
		}
		if d.Projects != nil {
			d.Projects.Routes(mux, protected)
		}
		if d.Environments != nil {
			d.Environments.Routes(mux, protected)
		}
		if d.Deployments != nil {
			d.Deployments.Routes(mux, protected)
		}
	}

	// Catch-all so unknown routes get the standard error envelope.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "resource not found")
	})

	return middleware.Chain(mux,
		middleware.RequestID,
		middleware.ClientIP,
		middleware.Logger(d.Logger),
		middleware.Recover(d.Logger, func(w http.ResponseWriter, r *http.Request) {
			httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "internal server error")
		}),
		middleware.SecureHeaders,
	)
}

// handleLiveness answers "is the process alive?". It must not check
// dependencies: a database outage should not make Kubernetes restart the API.
func handleLiveness(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
	}
}

// handleReadiness answers "can this instance serve traffic?". A failing
// critical check returns 503 so the load balancer routes traffic elsewhere;
// a failing soft check reports "degraded" but keeps the instance in service.
func handleReadiness(logger *slog.Logger, checks, soft map[string]ReadinessCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		results := make(map[string]string, len(checks)+len(soft))
		run := func(set map[string]ReadinessCheck) (healthy bool) {
			healthy = true
			for name, check := range set {
				if err := check(ctx); err != nil {
					healthy = false
					results[name] = "unavailable"
					logger.WarnContext(ctx, "readiness check failed", "check", name, "err", err)
					continue
				}
				results[name] = "ok"
			}
			return healthy
		}
		ready, softOK := run(checks), run(soft)

		switch {
		case !ready:
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "checks": results})
		case !softOK:
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "degraded", "checks": results})
		default:
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "checks": results})
		}
	}
}
