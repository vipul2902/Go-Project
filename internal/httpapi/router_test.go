package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/httpx"
	"github.com/vipul2902/clouddeploy/internal/middleware"
)

func newTestRouter(checks map[string]ReadinessCheck) http.Handler {
	return NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		Checks:  checks,
	})
}

func do(t *testing.T, h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLiveness(t *testing.T) {
	rec := do(t, newTestRouter(nil), http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["version"] != "test" {
		t.Errorf("unexpected body: %v", body)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("secure headers missing")
	}
}

func TestReadiness(t *testing.T) {
	ok := func(context.Context) error { return nil }
	fail := func(context.Context) error { return errors.New("connection refused") }

	tests := []struct {
		name   string
		checks map[string]ReadinessCheck
		want   int
	}{
		{"no checks", nil, http.StatusOK},
		{"all healthy", map[string]ReadinessCheck{"db": ok}, http.StatusOK},
		{"one failing", map[string]ReadinessCheck{"db": ok, "cache": fail}, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, newTestRouter(tt.checks), http.MethodGet, "/readyz", nil)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			// Internal error detail must never leak to clients.
			if body := rec.Body.String(); strings.Contains(body, "connection refused") {
				t.Errorf("internal error leaked: %s", body)
			}
		})
	}
}

func TestNotFoundUsesErrorEnvelope(t *testing.T) {
	rec := do(t, newTestRouter(nil), http.MethodGet, "/nope", map[string]string{middleware.HeaderRequestID: "abc-123"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body httpx.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != httpx.CodeNotFound || body.Error.RequestID != "abc-123" {
		t.Errorf("unexpected error body: %+v", body)
	}
}

func TestRequestIDGeneratedWhenInvalid(t *testing.T) {
	rec := do(t, newTestRouter(nil), http.MethodGet, "/healthz", map[string]string{middleware.HeaderRequestID: "bad id\nwith newline"})
	id := rec.Header().Get(middleware.HeaderRequestID)
	if len(id) != 32 {
		t.Errorf("expected generated 32-char ID, got %q", id)
	}
}

func TestPanicReturns500Envelope(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := middleware.Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }),
		middleware.RequestID,
		middleware.Recover(logger, func(w http.ResponseWriter, r *http.Request) {
			httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, "internal server error")
		}),
	)
	rec := do(t, h, http.MethodGet, "/", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Error("panic value leaked to client")
	}
}
