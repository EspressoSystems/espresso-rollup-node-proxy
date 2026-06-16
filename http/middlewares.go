package http

import (
	"net/http"

	"github.com/ethereum/go-ethereum/log"
)

// HTTPRPCMiddlewares is a helper function that applies all the middlewares
// necessary for handling JSON-RPC requests over HTTP.
func HTTPRPCMiddlewares(logger log.Logger, maxRequestBodySize int64, handler http.Handler) http.Handler {
	h := handler

	h = ContentTypeIsJSONRPCMiddleware(h, logger)
	h = AutoBodyCloserMiddleware(h, logger)
	if maxRequestBodySize > 0 {
		h = RequestBodySizeLimiterMiddleware(h, logger, maxRequestBodySize)
	}
	h = MethodIsMiddleware(h, http.MethodPost)
	h = RecoveryMiddleware(h, logger)

	return h
}
