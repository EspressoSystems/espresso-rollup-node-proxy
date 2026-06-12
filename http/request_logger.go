package http

import (
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader implements [http.ResponseWriter].
//
// This method stores the code parameter passed to it for future inspection.
func (w *statusResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// httpRequestLoggingMiddleware is a middleware that logs incoming HTTP
// requests and the status and processing time of the corresponding responses.
type httpRequestLoggingMiddleware struct {
	handler http.Handler
	logger  log.Logger
}

// ServeHttp implements http.Handler
//
// This times the request, and passes the request through to the next handler.
// Once the request is processed, it will log the results.
func (m *httpRequestLoggingMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
	m.handler.ServeHTTP(sw, r)
	m.logger.Debug("http request",
		"method", r.Method,
		"path", r.URL.Path,
		"remote_addr", r.RemoteAddr,
		"status", sw.statusCode,
		"latency_ms", time.Since(start).Milliseconds(),
		"content_length", r.ContentLength,
	)
}

// RequestLoggingMiddleware is a middleware that logs incoming HTTP requests
// and the status and processing time of the corresponding responses.
func RequestLoggingMiddleware(next http.Handler, logger log.Logger) http.Handler {
	return &httpRequestLoggingMiddleware{handler: next, logger: logger}
}
