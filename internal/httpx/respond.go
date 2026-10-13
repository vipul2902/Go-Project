// Package httpx holds HTTP helpers shared by every handler package:
// the JSON response/error envelope and safe request decoding.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/vipul2902/clouddeploy/internal/middleware"
)

// Error codes returned to clients. Codes are stable identifiers that clients
// may branch on; messages are human-readable and may change.
const (
	CodeNotFound        = "NOT_FOUND"
	CodeBadRequest      = "BAD_REQUEST"
	CodeValidation      = "VALIDATION_FAILED"
	CodeUnauthorized    = "UNAUTHORIZED"
	CodeForbidden       = "FORBIDDEN"
	CodeConflict        = "CONFLICT"
	CodePayloadTooLarge = "PAYLOAD_TOO_LARGE"
	CodeInternal        = "INTERNAL_ERROR"
	CodeUnavailable     = "SERVICE_UNAVAILABLE"
)

// ErrorBody is the standard error envelope.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes one error. Details maps field names to problems for
// validation errors.
type ErrorDetail struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
	}
}

// WriteError writes the standard error envelope. The message must be safe to
// show to clients; internal details belong in server logs only.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeError(w, r, status, ErrorDetail{Code: code, Message: message})
}

// WriteErrorDetails writes an error with machine-readable details.
func WriteErrorDetails(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]string) {
	writeError(w, r, status, ErrorDetail{Code: code, Message: message, Details: details})
}

// WriteValidationError writes a 422 with per-field problems.
func WriteValidationError(w http.ResponseWriter, r *http.Request, details map[string]string) {
	writeError(w, r, http.StatusUnprocessableEntity, ErrorDetail{
		Code: CodeValidation, Message: "request validation failed", Details: details,
	})
}

// WriteInternalError logs err with the request ID and returns a generic 500.
func WriteInternalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "internal error",
		"request_id", middleware.RequestIDFrom(r.Context()), "path", r.URL.Path, "err", err)
	WriteError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error")
}

func writeError(w http.ResponseWriter, r *http.Request, status int, d ErrorDetail) {
	d.RequestID = middleware.RequestIDFrom(r.Context())
	WriteJSON(w, status, ErrorBody{Error: d})
}
