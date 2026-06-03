package http

import (
	"net/http"

	"github.com/ethereum/go-ethereum/log"
)

func HTTPRPCMiddlewares(logger log.Logger, maxRequestBodySize int64, handler http.Handler) http.Handler {
	var h http.Handler = handler

	h = ContentTypeIsJSONRPCMiddleware(h, logger)
	if maxRequestBodySize > 0 {
		h = RequestBodySizeLimiterMiddleware(h, logger, maxRequestBodySize)
	}
	h = MethodIsMiddleware(h, http.MethodPost)
	h = RecoveryMiddleware(h, logger)

	return h
}
