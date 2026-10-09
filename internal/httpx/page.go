package httpx

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
)

// Pagination bounds.
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// Page holds parsed pagination query parameters (?limit=&cursor=).
type Page struct {
	Limit  int
	Cursor string
}

// List is the envelope for paginated responses. NextCursor is null on the
// last page.
type List[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

// ParsePage reads limit and cursor, returning field errors for bad input.
func ParsePage(r *http.Request) (Page, map[string]string) {
	p := Page{Limit: DefaultLimit, Cursor: r.URL.Query().Get("cursor")}
	errs := map[string]string{}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > MaxLimit {
			errs["limit"] = fmt.Sprintf("must be an integer between 1 and %d", MaxLimit)
		} else {
			p.Limit = n
		}
	}
	return p, errs
}

// EncodeCursor serializes a keyset position into an opaque token. Opaque
// cursors let the server change their contents without breaking clients.
func EncodeCursor(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a cursor produced by EncodeCursor.
func DecodeCursor(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s is a well-formed UUID. Validating IDs before
// they reach SQL turns garbage IDs into 404s instead of database errors.
func IsUUID(s string) bool { return uuidRE.MatchString(s) }

// PathUUID returns the named path value if it is a UUID; otherwise it writes
// a 404 and returns false.
func PathUUID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	v := r.PathValue(name)
	if !IsUUID(v) {
		WriteError(w, r, http.StatusNotFound, CodeNotFound, "resource not found")
		return "", false
	}
	return v, true
}
