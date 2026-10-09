package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MaxBodyBytes bounds request bodies so a client cannot exhaust memory.
const MaxBodyBytes = 1 << 20 // 1 MiB

// DecodeJSON decodes exactly one JSON object into dst, rejecting unknown
// fields and oversized bodies. On failure it writes the error response and
// returns false, so handlers can simply `if !DecodeJSON(...) { return }`.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		WriteError(w, r, http.StatusUnsupportedMediaType, CodeBadRequest, "Content-Type must be application/json")
		return false
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &maxErr):
			WriteError(w, r, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "request body too large")
		case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
			WriteError(w, r, http.StatusBadRequest, CodeBadRequest, "malformed JSON")
		case errors.As(err, &typeErr):
			WriteError(w, r, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("field %q has the wrong type", typeErr.Field))
		case errors.Is(err, io.EOF):
			WriteError(w, r, http.StatusBadRequest, CodeBadRequest, "request body is empty")
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			WriteError(w, r, http.StatusBadRequest, CodeBadRequest, strings.TrimPrefix(err.Error(), "json: "))
		default:
			WriteError(w, r, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		}
		return false
	}
	if dec.More() {
		WriteError(w, r, http.StatusBadRequest, CodeBadRequest, "body must contain a single JSON object")
		return false
	}
	return true
}
