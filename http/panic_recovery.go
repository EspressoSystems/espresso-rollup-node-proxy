package http

import (
	"net/http"
	"runtime/debug"

	"github.com/ethereum/go-ethereum/log"
)

// that may occur in the process of processing a request.
//
// While we do not expect any panics to reasonably occur, we don't want a
// missed runtime panic to take the server down unnecessarily.
type httpPanicRecoveryMiddleware struct {
	handler http.Handler
	logger  log.Logger
}

// ServeHttp implements http.Handler
//
// This automatically rocvers and captures panics that occur when the
// underlying http.Handler is processing the request.
//
// NOTE: Panic recovery handling is not required in golang's ServeHTTP
// methods, as there is automatic panic recovery handling built into
// the http.Server itself.
func (m *httpPanicRecoveryMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			m.logger.Error("panic recovered in HTTP handler", "panic", rec, "stack", string(debug.Stack()))
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}()
	m.handler.ServeHTTP(w, r)
}

// RecoveryMiddleware is a middleware that cleanly handles panics that may
// occur.
func RecoveryMiddleware(next http.Handler, logger log.Logger) http.Handler {
	return &httpPanicRecoveryMiddleware{handler: next, logger: logger}
}
