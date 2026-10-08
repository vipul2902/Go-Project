// Package httpapi wires HTTP routes and provides shared response helpers.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/vipul2902/clouddeploy/internal/middleware"
)

// Error codes returned to clients. Codes are stable identifiers that clients
// may branch on; messages are human-readable and may change.
const (
	CodeNotFound         = "NOT_FOUND"
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	CodeInternal         = "INTERNAL_ERROR"
	CodeUnavailable      = "SERVICE_UNAVAILABLE"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
	}
}

// WriteError writes the standard error envelope. The message must be safe to
// show to clients; internal details belong in server logs only.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	WriteJSON(w, status, errorBody{Error: errorDetail{
		Code:      code,
		Message:   message,
		RequestID: middleware.RequestIDFrom(r.Context()),
	}})
}
