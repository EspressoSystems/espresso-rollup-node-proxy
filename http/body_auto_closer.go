package http

import (
	"net/http"

	"github.com/ethereum/go-ethereum/log"
)

// httpAutoBodyCloserMiddleware is a middleware that automatically closes the
// the body of the http.Request
type httpAutoBodyCloserMiddleware struct {
	handler http.Handler
	logger  log.Logger
}

func (m *httpAutoBodyCloserMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.handler.ServeHTTP(w, r)
	// Body is guaranteed not to be `nil` based on documentation annotation
	// on the `Body` field within `http.Request`.
	if r.Body == nil {
		return
	}

	// Additionally, according to the documentation some more, calling `Close`
	// on the `request.Body` is not necessary.
	if err := r.Body.Close(); err != nil {
		m.logger.Warn("failed to close request body", "error", err)
	}
}

// AutoBodyCloserMiddleware is a middleware that automatically closes the
// body of request when the request is completed.
func AutoBodyCloserMiddleware(next http.Handler, logger log.Logger) http.Handler {
	return &httpAutoBodyCloserMiddleware{handler: next, logger: logger}
}
