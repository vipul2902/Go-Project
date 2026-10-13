package httpx

import "net/http"

// Router is the subset of *http.ServeMux that handler packages register
// routes on. It lets the composition root record every registered pattern
// (see httpapi.Routes), which tests use to check that no route is
// accidentally reachable without authentication.
type Router interface {
	Handle(pattern string, handler http.Handler)
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}
