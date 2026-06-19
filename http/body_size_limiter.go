package http

import (
	"net/http"
)

// httpBodySizeLimiterMiddleware is a middleware that limits the size of the
// of the request body to a specified maximum size. It also checks the
// Content-Length of the request and will return an error if the request
// should be found to be too large.
type httpBodySizeLimiterMiddleware struct {
	handler            http.Handler
	maxRequestBodySize int64
}

// ServeHTTP implements http.Handler
//
// This middleware replaces the Request body's io.ReaderCloser with a different
// ReadCloser that limits the size of the content body.
//
// Additionally, it inspects the `Content-Length` header and will inform the
// user if the length of the request body is too long to be processed.
func (m *httpBodySizeLimiterMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Replace the request body with a limited reader that enforces the maximum
	// request body size.

	r.Body = http.MaxBytesReader(w, r.Body, m.maxRequestBodySize)

	if r.ContentLength > m.maxRequestBodySize {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}

	m.handler.ServeHTTP(w, r)
}

// RequestBodySizeLimiterMiddleware is a middleware that limits the size of
// the request body to a specified maximum size. It also checks the
// Content-Length header of the request and will return an error if the
// request should be larger than the maximum size.
func RequestBodySizeLimiterMiddleware(next http.Handler, maxRequestBodySize int64) http.Handler {
	return &httpBodySizeLimiterMiddleware{
		handler:            next,
		maxRequestBodySize: maxRequestBodySize,
	}
}
