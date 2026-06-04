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

// ServeHttp implements http.Handler
//
// This method automatically closes the Body after the handler has completed
// the request.
func (m *httpAutoBodyCloserMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		// Body is guaranteed not to be [nil] for ServeHTTP based on
		// documentation annotation on the `Body` field within [http.Request]:
		// https://cs.opensource.google/go/go/+/refs/tags/go1.25.0:src/net/http/request.go;l=180;drc=b2819d13dbe19343426e688da4ddfeb57c8589fc
		if r.Body == nil {
			return
		}

		// Additionally, according to the documentation some more, calling [Close]
		// on the [request.Body] is not necessary:
		// https://cs.opensource.google/go/go/+/refs/tags/go1.25.0:src/net/http/request.go;l=182-183;drc=b2819d13dbe19343426e688da4ddfeb57c8589fc
		if err := r.Body.Close(); err != nil {
			m.logger.Warn("failed to close request body", "error", err)
		}
	}()

	m.handler.ServeHTTP(w, r)
}

// AutoBodyCloserMiddleware is a middleware that automatically closes the
// body of request when the request is completed.
func AutoBodyCloserMiddleware(next http.Handler, logger log.Logger) http.Handler {
	return &httpAutoBodyCloserMiddleware{handler: next, logger: logger}
}
