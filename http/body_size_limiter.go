package http

import (
	"io"
	"net/http"
	"strconv"

	"proxy/jsonrpcv2"

	"github.com/ethereum/go-ethereum/log"
)

type readCloser struct {
	io.Reader
	io.Closer
}

// httpBodySizeLimiterMiddleware is a middleware that limits the size of the
// of the request body to a specified maximum size. It also checks the
// Content-Length of the request and will return an error if the request
// should be found to be too large.
type httpBodySizeLimiterMiddleware struct {
	handler            http.Handler
	logger             log.Logger
	maxRequestBodySize int64
}

// ServeHttp implements http.Handler
//
// This middleware replaces the Request body's io.ReaderCloser with a different
// ReadCloser that limits the size of the content body.
//
// Additionally, it inspects the `Content-Length` header and will inform the
// user if the length of the request body is too long to be processed.
func (m *httpBodySizeLimiterMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Replace the request body with a limited reader that enforces the maximum
	// request body size.
	r.Body = &readCloser{
		Reader: io.LimitReader(r.Body, m.maxRequestBodySize),
		Closer: r.Body,
	}

	if contentLengthString := r.Header.Get("Content-Length"); contentLengthString != "" {
		contentLength, err := strconv.ParseInt(contentLengthString, 10, 64)
		if err == nil {
			WriteJSONRPCError(
				w,
				nil,
				jsonrpcv2.CodeInvalidRequest,
				"Unable to determine content length of request body",
			)
			return
		}

		if contentLength > m.maxRequestBodySize {
			WriteJSONRPCError(
				w,
				nil,
				jsonrpcv2.CodeInvalidRequest,
				"content length is too large",
			)
			return
		}
	}

	m.handler.ServeHTTP(w, r)
}

// RequestBodySizeLimiterMiddleware is a middleware that limits the size of
// the request body to a specified maximum size. It also checks the
// Content-Length header of the request and will return an error if the
// request should be larger than the maximum size.
func RequestBodySizeLimiterMiddleware(next http.Handler, logger log.Logger, maxRequestBodySize int64) http.Handler {
	return &httpBodySizeLimiterMiddleware{
		handler:            next,
		logger:             logger,
		maxRequestBodySize: maxRequestBodySize,
	}
}
