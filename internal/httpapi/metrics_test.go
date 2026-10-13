package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Route labels must be patterns: two different deployment IDs produce ONE
// series. Raw paths would create a series per ID (unbounded cardinality).
func TestHTTPMetricsUseRoutePatterns(t *testing.T) {
	reg := prometheus.NewRegistry()
	h, err := NewApp(nil, AppConfig{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), JWTSecret: []byte("metrics-test-secret-at-least-32-bytes"),
		AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, Metrics: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/deployments/00000000-0000-4000-8000-000000000001",
		"/api/v1/deployments/00000000-0000-4000-8000-000000000002",
		"/api/v1/deployments/00000000-0000-4000-8000-000000000003/logs?follow=true",
		"/no/such/route",
		"/healthz",
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	exp := `
# HELP http_requests_total HTTP requests by method, route pattern and status code.
# TYPE http_requests_total counter
http_requests_total{code="200",method="GET",route="GET /healthz"} 1
http_requests_total{code="401",method="GET",route="GET /api/v1/deployments/{id}"} 2
http_requests_total{code="401",method="GET",route="GET /api/v1/deployments/{id}/logs"} 1
http_requests_total{code="404",method="GET",route="/"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(exp), "http_requests_total"); err != nil {
		t.Error(err)
	}
	// SSE streams are counted but kept out of the latency histogram.
	if n := seriesCount(t, reg, "http_request_duration_seconds"); n != 3 { // healthz, {id}, catch-all
		t.Errorf("latency histogram has %d series, want 3 (no follow=true stream)", n)
	}
}

func seriesCount(t *testing.T, reg *prometheus.Registry, name string) int {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return len(mf.GetMetric())
		}
	}
	return 0
}
