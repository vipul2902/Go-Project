package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decode(t *testing.T, body, contentType string) (bool, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	var dst struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	return DecodeJSON(w, r, &dst), w
}

func TestDecodeJSONRejections(t *testing.T) {
	cases := []struct {
		name, body, ct string
		status         int
		msg            string
	}{
		{"wrong content type", `{"name":"x"}`, "text/plain", http.StatusUnsupportedMediaType, "Content-Type"},
		{"malformed", `{"name":`, "application/json", http.StatusBadRequest, "malformed JSON"},
		{"empty", ``, "application/json", http.StatusBadRequest, "empty"},
		{"unknown field (mass assignment)", `{"name":"x","is_admin":true}`, "application/json", http.StatusBadRequest, "is_admin"},
		{"wrong type", `{"n":"seven"}`, "application/json", http.StatusBadRequest, "wrong type"},
		{"two objects", `{"name":"a"}{"name":"b"}`, "application/json", http.StatusBadRequest, "single JSON object"},
		{"too large", `{"name":"` + strings.Repeat("x", MaxBodyBytes) + `"}`, "application/json", http.StatusRequestEntityTooLarge, "too large"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, w := decode(t, c.body, c.ct)
			if ok || w.Code != c.status || !strings.Contains(w.Body.String(), c.msg) {
				t.Errorf("ok=%v status=%d body=%s", ok, w.Code, w.Body.String())
			}
		})
	}
	if ok, w := decode(t, `{"name":"x","n":1}`, "application/json; charset=utf-8"); !ok {
		t.Errorf("valid body rejected: %s", w.Body.String())
	}
}

// Internal errors must never reach the client: only a generic message and
// the request ID (to correlate with server logs).
func TestWriteInternalErrorHidesDetails(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	WriteInternalError(w, r, errors.New(`pq: relation "users" password=hunter2 at 10.0.0.5`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", w.Code)
	}
	for _, leak := range []string{"pq:", "users", "hunter2", "10.0.0.5"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("leaked %q: %s", leak, w.Body.String())
		}
	}
	var body ErrorBody
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil || body.Error.Code != CodeInternal {
		t.Errorf("envelope: %+v %v", body, err)
	}
}

func TestSSEFraming(t *testing.T) {
	w := httptest.NewRecorder()
	s, err := StartSSE(w)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Event("log", "42", map[string]string{"m": "a\nb"}) // newline inside data must stay one data line (JSON-escaped)
	_ = s.Heartbeat()
	want := "id: 42\nevent: log\ndata: {\"m\":\"a\\nb\"}\n\n: keep-alive\n\n"
	if w.Body.String() != want || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("stream = %q headers=%v", w.Body.String(), w.Header())
	}
}

func FuzzDecodeCursor(f *testing.F) {
	f.Add(EncodeCursor(map[string]any{"id": "x", "v": 1}))
	f.Add("")
	f.Add("!!!not-base64")
	f.Fuzz(func(t *testing.T, s string) {
		var v struct {
			ID string `json:"id"`
		}
		_ = DecodeCursor(s, &v) // must never panic on attacker-supplied cursors
	})
}
