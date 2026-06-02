package http

import (
	"fmt"
	"net/http"
)

// httpEnsureMethodMiddleware is a middleware that checks the HTTP method
// of incoming requests and ensures that it matches the expected method. If
// the method does not match, it responds with a 405 Method Not Allowed error.
type httpEnsureMethodMiddleware struct {
	handler http.Handler
	method  string
}

// ServeHTTP implemets http.Handler
//
// This checks the requested method of the incoming request.  If it does not
// match the expected method, then a Method Not Allowed HTTP Response is
// returned.
func (m *httpEnsureMethodMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != m.method {
		http.Error(w, fmt.Sprintf("only %s method is allowed", m.method), http.StatusMethodNotAllowed)
		return
	}

	m.handler.ServeHTTP(w, r)
}

// MethodIsMiddleware is a middleware that ensures that incoming HTTP
// requests use the specified method.
func MethodIsMiddleware(next http.Handler, method string) http.Handler {
	return &httpEnsureMethodMiddleware{handler: next, method: method}
}
