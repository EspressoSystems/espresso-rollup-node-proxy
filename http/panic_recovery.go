package http

import (
	"net/http"
	"runtime/debug"

	"github.com/ethereum/go-ethereum/log"
)

// httpPanicRecoveryMiddleware is a middleware that cleanly handles panics
// that may occur in the process of processing a request.
//
// While we do not expect any panics to reasonably occur, we don't want a
// missed runtime panic to take the server down unnecessarily.
type httpPanicRecoveryMiddleware struct {
	handler http.Handler
	logger  log.Logger
}

// trackingResponseWriter wraps http.ResponseWriter and records whether the
// response headers have been committed by a WriteHeader or Write call.
type trackingResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

// WriteHeader implements http.ResponseWriter and marks headers as committed.
func (t *trackingResponseWriter) WriteHeader(code int) {
	t.wroteHeader = true
	t.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter and marks headers as committed on first
// write, since net/http flushes headers implicitly on the first body write.
func (t *trackingResponseWriter) Write(b []byte) (int, error) {
	t.wroteHeader = true
	return t.ResponseWriter.Write(b)
}

// ServeHTTP implements http.Handler
//
// This automatically recovers and captures panics that occur when the
// underlying http.Handler is processing the request.
//
// NOTE: Panic recovery handling is not required in golang's ServeHTTP
// methods, as there is automatic panic recovery handling built into
// the http.Server itself.
func (m *httpPanicRecoveryMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tw := &trackingResponseWriter{ResponseWriter: w}
	defer func() {
		if rec := recover(); rec != nil {
			m.logger.Error("panic recovered in HTTP handler", "panic", rec, "stack", string(debug.Stack()))
			if !tw.wroteHeader {
				http.Error(tw, "internal server error", http.StatusInternalServerError)
			}
		}
	}()
	m.handler.ServeHTTP(tw, r)
}

// RecoveryMiddleware is a middleware that cleanly handles panics that may
// occur.
func RecoveryMiddleware(next http.Handler, logger log.Logger) http.Handler {
	return &httpPanicRecoveryMiddleware{handler: next, logger: logger}
}
