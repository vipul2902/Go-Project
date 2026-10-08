package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/vipul2902/clouddeploy/internal/middleware"
)

// ReadinessCheck reports whether a dependency (database, cache, ...) is usable.
type ReadinessCheck func(ctx context.Context) error

// Deps are the dependencies the HTTP layer needs. Later phases add services here.
type Deps struct {
	Logger  *slog.Logger
	Version string
	// Checks are run by /readyz; keyed by dependency name.
	Checks map[string]ReadinessCheck
}

// NewRouter builds the root handler with all routes and middleware.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handleLiveness(d.Version))
	mux.HandleFunc("GET /readyz", handleReadiness(d.Logger, d.Checks))

	// Catch-all so unknown routes get the standard error envelope.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, http.StatusNotFound, CodeNotFound, "resource not found")
	})

	return middleware.Chain(mux,
		middleware.RequestID,
		middleware.Logger(d.Logger),
		middleware.Recover(d.Logger, func(w http.ResponseWriter, r *http.Request) {
			WriteError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error")
		}),
		middleware.SecureHeaders,
	)
}

// handleLiveness answers "is the process alive?". It must not check
// dependencies: a database outage should not make Kubernetes restart the API.
func handleLiveness(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
	}
}

// handleReadiness answers "can this instance serve traffic?". A failing check
// returns 503 so the load balancer routes traffic elsewhere.
func handleReadiness(logger *slog.Logger, checks map[string]ReadinessCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		results := make(map[string]string, len(checks))
		ready := true
		for name, check := range checks {
			if err := check(ctx); err != nil {
				ready = false
				results[name] = "unavailable"
				logger.WarnContext(ctx, "readiness check failed", "check", name, "err", err)
				continue
			}
			results[name] = "ok"
		}

		if !ready {
			WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "checks": results})
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "checks": results})
	}
}
